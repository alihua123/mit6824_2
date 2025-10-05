package kvraft

import (
	"crypto/rand"
	"log"
	"math/big"
	"time"

	"6.5840/labrpc"
)

// Clerk 客户端结构体
// 负责与KVRaft集群通信，提供线性一致的KV操作接口
type Clerk struct {
	servers []*labrpc.ClientEnd // KVServer集群的RPC端点

	// 客户端状态管理
	clientId   int64 // 客户端唯一标识符，用于幂等性保证
	seqNum     int64 // 请求序列号，单调递增，用于去重
	lastLeader int   // 上次成功联系的Leader，用于优化重试
}

// nrand 生成加密安全的随机数
func nrand() int64 {
	max := big.NewInt(int64(1) << 62)
	bigx, _ := rand.Int(rand.Reader, max)
	x := bigx.Int64()
	return x
}

// MakeClerk 创建新的KV客户端
func MakeClerk(servers []*labrpc.ClientEnd) *Clerk {
	ck := new(Clerk)
	ck.servers = servers
	ck.clientId = nrand() // 生成唯一的客户端ID
	ck.seqNum = 0
	ck.lastLeader = 0

	log.Printf("[Client %d] 客户端创建完成，连接到 %d 个服务器", ck.clientId, len(servers))
	return ck
}

// Get 获取指定键的值
// 如果键不存在返回空字符串
// 会一直重试直到成功（处理网络错误、Leader变更等情况）
func (ck *Clerk) Get(key string) string {
	// 递增序列号
	ck.seqNum++
	seqNum := ck.seqNum
	
	log.Printf("[Client %d] 开始 Get 操作: Key=%s, SeqNum=%d", ck.clientId, key, seqNum)

	args := GetArgs{
		Key:      key,
		ClientId: ck.clientId,
		SeqNum:   seqNum,
	}

	// 从上次成功的Leader开始尝试
	serverIndex := ck.lastLeader

	for {
		var reply GetReply

		log.Printf("[Client %d] 尝试联系 Server %d 进行 Get", ck.clientId, serverIndex)
		ok := ck.servers[serverIndex].Call("KVServer.Get", &args, &reply)

		if ok {
			switch reply.Err {
			case OK:
				// 成功获取值
				ck.lastLeader = serverIndex // 记住这个Leader
				log.Printf("[Client %d] Get 成功: Key=%s, Value=%s", ck.clientId, key, reply.Value)
				return reply.Value

			case ErrNoKey:
				// 键不存在，这也是一个有效的结果
				ck.lastLeader = serverIndex
				log.Printf("[Client %d] Get 完成，键不存在: Key=%s", ck.clientId, key)
				return ""

			case ErrWrongLeader:
				// 不是Leader，尝试下一个服务器
				log.Printf("[Client %d] Server %d 不是 Leader，尝试下一个", ck.clientId, serverIndex)

			case ErrTimeout:
				// 超时，可能是网络问题或系统过载
				log.Printf("[Client %d] Server %d 超时，尝试下一个", ck.clientId, serverIndex)

			default:
				log.Printf("[Client %d] Server %d 返回未知错误: %s", ck.clientId, serverIndex, reply.Err)
			}
		} else {
			// RPC调用失败（网络错误等）
			log.Printf("[Client %d] 无法联系 Server %d", ck.clientId, serverIndex)
		}

		// 尝试下一个服务器
		serverIndex = (serverIndex + 1) % len(ck.servers)

		// 短暂等待后重试
		time.Sleep(100 * time.Millisecond)
	}
}

// PutAppend 执行Put或Append操作
// Put操作：设置键的值
// Append操作：在键的现有值后追加内容
// 会一直重试直到成功
func (ck *Clerk) PutAppend(key string, value string, op string) {
	// 递增序列号
	ck.seqNum++
	seqNum := ck.seqNum

	log.Printf("[Client %d] 开始 %s 操作: Key=%s, Value=%s, SeqNum=%d",
		ck.clientId, op, key, value, seqNum)

	args := PutAppendArgs{
		Key:      key,
		Value:    value,
		Op:       op,
		ClientId: ck.clientId,
		SeqNum:   seqNum,
	}

	// 从上次成功的Leader开始尝试
	serverIndex := ck.lastLeader

	for {
		var reply PutAppendReply

		log.Printf("[Client %d] 尝试联系 Server %d 进行 %s", ck.clientId, serverIndex, op)
		ok := ck.servers[serverIndex].Call("KVServer.PutAppend", &args, &reply)

		if ok {
			switch reply.Err {
			case OK:
				// 操作成功完成
				ck.lastLeader = serverIndex // 记住这个Leader
				log.Printf("[Client %d] %s 成功: Key=%s, Value=%s", ck.clientId, op, key, value)
				return

			case ErrWrongLeader:
				// 不是Leader，尝试下一个服务器
				log.Printf("[Client %d] Server %d 不是 Leader，尝试下一个", ck.clientId, serverIndex)

			case ErrTimeout:
				// 超时，可能是网络问题或系统过载
				log.Printf("[Client %d] Server %d 超时，尝试下一个", ck.clientId, serverIndex)

			default:
				log.Printf("[Client %d] Server %d 返回未知错误: %s", ck.clientId, serverIndex, reply.Err)
			}
		} else {
			// RPC调用失败（网络错误等）
			log.Printf("[Client %d] 无法联系 Server %d", ck.clientId, serverIndex)
		}

		// 尝试下一个服务器
		serverIndex = (serverIndex + 1) % len(ck.servers)

		// 短暂等待后重试
		time.Sleep(100 * time.Millisecond)
	}
}

// Put 设置键的值
func (ck *Clerk) Put(key string, value string) {
	ck.PutAppend(key, value, "Put")
}

// Append 在键的现有值后追加内容
func (ck *Clerk) Append(key string, value string) {
	ck.PutAppend(key, value, "Append")
}
