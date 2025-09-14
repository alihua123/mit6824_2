package kvsrv

import "6.5840/labrpc"
import "crypto/rand"
import "math/big"
import "sync/atomic"

// Clerk 客户端结构体
type Clerk struct {
	server *labrpc.ClientEnd // RPC连接端点
	
	// 【关键点】：客户端唯一标识符，用于服务器端的重复检测
	clientId int64
	
	// 【关键点】：请求序列号，确保每个请求都有唯一标识
	requestId int64
}

// nrand 生成随机数
func nrand() int64 {
	max := big.NewInt(int64(1) << 62)
	bigx, _ := rand.Int(rand.Reader, max)
	x := bigx.Int64()
	return x
}

// MakeClerk 创建新的客户端
// 【关键点】：为每个客户端分配唯一的ID
func MakeClerk(server *labrpc.ClientEnd) *Clerk {
	ck := new(Clerk)
	ck.server = server
	
	// 生成唯一的客户端ID
	ck.clientId = nrand()
	
	// 初始化请求ID
	ck.requestId = 0
	
	return ck
}

// Get 获取键对应的值
// 【关键点】：包含重试机制，处理网络故障
func (ck *Clerk) Get(key string) string {
	// 准备RPC参数
	args := GetArgs{
		Key:       key,
		ClientId:  ck.clientId,
		RequestId: atomic.AddInt64(&ck.requestId, 1),
	}
	
	// 【重试机制】：持续重试直到成功
	for {
		reply := GetReply{}
		
		// 发送RPC请求
		ok := ck.server.Call("KVServer.Get", &args, &reply)
		
		if ok {
			// RPC成功，返回结果
			return reply.Value
		}
		
		// RPC失败，继续重试
		// 在实际系统中，这里可能需要添加退避策略
	}
}

// PutAppend Put和Append操作的共同实现
// 【关键点】：包含重复检测和重试机制
func (ck *Clerk) PutAppend(key string, value string, op string) string {
	// 准备RPC参数
	args := PutAppendArgs{
		Key:       key,
		Value:     value,
		ClientId:  ck.clientId,
		RequestId: atomic.AddInt64(&ck.requestId, 1),
	}
	
	// 【重试机制】：持续重试直到成功
	for {
		reply := PutAppendReply{}
		
		// 根据操作类型调用相应的RPC方法
		ok := ck.server.Call("KVServer."+op, &args, &reply)
		
		if ok {
			// RPC成功，返回结果
			return reply.Value
		}
		
		// RPC失败，继续重试
		// 【重要】：重试时使用相同的RequestId，确保重复检测正常工作
	}
}

// Put 设置键值对
func (ck *Clerk) Put(key string, value string) {
	ck.PutAppend(key, value, "Put")
}

// Append 追加值到键的现有值后面，并返回追加前的原始值
func (ck *Clerk) Append(key string, value string) string {
	return ck.PutAppend(key, value, "Append")
}
