package kvraft

/*
KVRaft 实现说明

本包实现了基于Raft一致性算法的分布式键值存储系统。
主要特性：
1. 线性一致性：所有操作都通过Raft协议达成一致后才执行
2. 容错性：可以容忍少数节点故障，系统仍能正常工作
3. 幂等性：重复的客户端请求不会被重复执行
4. 分区容忍性：网络分区时，多数派仍能提供服务

核心组件：
- KVServer：服务器端，维护KV存储和处理客户端请求
- Clerk：客户端，提供Get/Put/Append接口，自动处理Leader发现和重试
- Op：操作结构体，封装了所有需要通过Raft复制的操作

关键机制：
1. 请求流程：客户端请求 -> Leader接收 -> Raft复制 -> 状态机应用 -> 返回结果
2. 幂等性保证：通过ClientId+SeqNum唯一标识每个请求，防止重复执行
3. Leader发现：客户端自动尝试不同服务器，找到当前Leader
4. 线性一致性：只有被Raft提交的操作才会被应用到状态机

使用方式：
	servers := []*labrpc.ClientEnd{...} // KVServer集群的RPC端点
	clerk := MakeClerk(servers)
	clerk.Put("key1", "value1")
	value := clerk.Get("key1")
	clerk.Append("key1", "more")
*/

import (
	"6.5840/labgob"
	"6.5840/labrpc"
	"6.5840/raft"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const Debug = false

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug {
		log.Printf(format, a...)
	}
	return
}

// Op 表示一个操作，将被提交到 Raft 日志
type Op struct {
	OpType   string // "Get", "Put", "Append"
	Key      string
	Value    string
	ClientId int64 // 客户端唯一 ID，用于幂等性
	SeqNum   int64 // 客户端请求序列号，用于幂等性
}

// 等待结果的通道
type NotifyCh struct {
	Term  int    // 该操作提交时的任期
	Value string // Get 操作的返回值
	Err   Err    // 错误信息
}

type KVServer struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	dead    int32 // set by Kill()

	maxraftstate int // snapshot if log grows this big

	// KV 存储引擎：键值对数据
	kvStore map[string]string
	
	// 幂等性保证：记录每个客户端最后执行的请求序列号
	lastApplied map[int64]int64 // clientId -> seqNum
	
	// 幂等性保证：缓存每个客户端最后一次操作的结果
	lastResult map[int64]string // clientId -> result (for Get operations)
	
	// 等待通道：用于 RPC handler 等待操作被应用
	notifyChans map[int]chan NotifyCh // logIndex -> notifyChan
	
	// 最后应用的日志索引（用于快照）
	lastAppliedIndex int
}


// Get RPC 处理器
func (kv *KVServer) Get(args *GetArgs, reply *GetReply) {
	DPrintf("[Server %d] 收到 Get 请求: Key=%s, ClientId=%d, SeqNum=%d", 
		kv.me, args.Key, args.ClientId, args.SeqNum)
	
	// 检查是否是重复请求（幂等性处理）
	kv.mu.Lock()
	if lastSeq, exists := kv.lastApplied[args.ClientId]; exists && lastSeq >= args.SeqNum {
		// 返回缓存的结果
		reply.Value = kv.lastResult[args.ClientId]
		reply.Err = OK
		DPrintf("[Server %d] Get 请求是重复的，返回缓存结果: %s", kv.me, reply.Value)
		kv.mu.Unlock()
		return
	}
	kv.mu.Unlock()
	
	// 构造操作并提交到 Raft
	op := Op{
		OpType:   "Get",
		Key:      args.Key,
		ClientId: args.ClientId,
		SeqNum:   args.SeqNum,
	}
	
	// 执行操作并等待结果
	err, value := kv.waitForApply(op)
	reply.Err = err
	reply.Value = value
	
	DPrintf("[Server %d] Get 请求完成: Key=%s, Value=%s, Err=%s", 
		kv.me, args.Key, value, err)
}

// PutAppend RPC 处理器
func (kv *KVServer) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	DPrintf("[Server %d] 收到 %s 请求: Key=%s, Value=%s, ClientId=%d, SeqNum=%d", 
		kv.me, args.Op, args.Key, args.Value, args.ClientId, args.SeqNum)
	
	// 检查是否是重复请求（幂等性处理）
	kv.mu.Lock()
	if lastSeq, exists := kv.lastApplied[args.ClientId]; exists && lastSeq >= args.SeqNum {
		// 重复请求，直接返回成功
		reply.Err = OK
		DPrintf("[Server %d] %s 请求是重复的，直接返回成功", kv.me, args.Op)
		kv.mu.Unlock()
		return
	}
	kv.mu.Unlock()
	
	// 构造操作并提交到 Raft
	op := Op{
		OpType:   args.Op,
		Key:      args.Key,
		Value:    args.Value,
		ClientId: args.ClientId,
		SeqNum:   args.SeqNum,
	}
	
	// 执行操作并等待结果
	err, _ := kv.waitForApply(op)
	reply.Err = err
	
	DPrintf("[Server %d] %s 请求完成: Key=%s, Err=%s", 
		kv.me, args.Op, args.Key, err)
}

func (kv *KVServer) Put(args *PutAppendArgs, reply *PutAppendReply) {
	kv.PutAppend(args, reply)
}

func (kv *KVServer) Append(args *PutAppendArgs, reply *PutAppendReply) {
	kv.PutAppend(args, reply)
}

// the tester calls Kill() when a KVServer instance won't
// be needed again. for your convenience, we supply
// code to set rf.dead (without needing a lock),
// and a killed() method to test rf.dead in
// long-running loops. you can also add your own
// code to Kill(). you're not required to do anything
// about this, but it may be convenient (for example)
// to suppress debug output from a Kill()ed instance.
func (kv *KVServer) Kill() {
	atomic.StoreInt32(&kv.dead, 1)
	kv.rf.Kill()
	// Your code here, if desired.
}

func (kv *KVServer) killed() bool {
	z := atomic.LoadInt32(&kv.dead)
	return z == 1
}

// waitForApply 等待操作被应用到状态机
// 这是KVServer的核心方法，负责将操作提交到Raft并等待结果
func (kv *KVServer) waitForApply(op Op) (Err, string) {
	// 提交操作到Raft
	index, term, isLeader := kv.rf.Start(op)
	
	if !isLeader {
		return ErrWrongLeader, ""
	}
	
	DPrintf("[Server %d] 操作已提交到 Raft: index=%d, term=%d, op=%+v", 
		kv.me, index, term, op)
	
	// 创建通知通道
	kv.mu.Lock()
	notifyCh := make(chan NotifyCh, 1)
	kv.notifyChans[index] = notifyCh
	kv.mu.Unlock()
	
	// 等待操作被应用（带超时）
	select {
	case result := <-notifyCh:
		kv.mu.Lock()
		delete(kv.notifyChans, index)
		kv.mu.Unlock()
		
		// 检查任期是否匹配（防止旧Leader的延迟回复）
		if result.Term != term {
			return ErrWrongLeader, ""
		}
		
		return result.Err, result.Value
		
	case <-time.After(500 * time.Millisecond): // 500ms超时
		kv.mu.Lock()
		delete(kv.notifyChans, index)
		kv.mu.Unlock()
		
		return ErrTimeout, ""
	}
}

// applier 持续监听applyCh，将Raft提交的操作应用到状态机
// 这是保证线性一致性的关键组件
func (kv *KVServer) applier() {
	for !kv.killed() {
		select {
		case msg := <-kv.applyCh:
			if msg.CommandValid {
				kv.applyCommand(msg)
			} else if msg.SnapshotValid {
				kv.applySnapshot(msg)
			}
		}
	}
}

// applyCommand 应用单个命令到状态机
func (kv *KVServer) applyCommand(msg raft.ApplyMsg) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	
	// 防止重复应用（Raft保证了顺序性，但我们要防御性编程）
	if msg.CommandIndex <= kv.lastAppliedIndex {
		DPrintf("[Server %d] 忽略已应用的命令: index=%d, lastApplied=%d", 
			kv.me, msg.CommandIndex, kv.lastAppliedIndex)
		return
	}
	
	kv.lastAppliedIndex = msg.CommandIndex
	
	// 解析操作
	op, ok := msg.Command.(Op)
	if !ok {
		DPrintf("[Server %d] 无效的命令类型: %T", kv.me, msg.Command)
		return
	}
	
	DPrintf("[Server %d] 应用命令: index=%d, op=%+v", kv.me, msg.CommandIndex, op)
	
	var result NotifyCh
	currentTerm, _ := kv.rf.GetState()
	result.Term = currentTerm
	
	// 检查是否是重复请求
	if lastSeq, exists := kv.lastApplied[op.ClientId]; exists && lastSeq >= op.SeqNum {
		// 重复请求，返回缓存结果
		result.Err = OK
		if op.OpType == "Get" {
			result.Value = kv.lastResult[op.ClientId]
		}
		DPrintf("[Server %d] 检测到重复请求: ClientId=%d, SeqNum=%d", 
			kv.me, op.ClientId, op.SeqNum)
	} else {
		// 新请求，执行操作
		result.Err, result.Value = kv.executeOperation(op)
		
		// 更新客户端状态
		kv.lastApplied[op.ClientId] = op.SeqNum
		if op.OpType == "Get" {
			kv.lastResult[op.ClientId] = result.Value
		}
	}
	
	// 通知等待的RPC处理器
	if notifyCh, exists := kv.notifyChans[msg.CommandIndex]; exists {
		select {
		case notifyCh <- result:
		default:
			// 通道已满或已关闭，忽略
		}
	}
}

// executeOperation 执行具体的KV操作
func (kv *KVServer) executeOperation(op Op) (Err, string) {
	switch op.OpType {
	case "Get":
		if value, exists := kv.kvStore[op.Key]; exists {
			DPrintf("[Server %d] Get 成功: Key=%s, Value=%s", kv.me, op.Key, value)
			return OK, value
		} else {
			DPrintf("[Server %d] Get 失败，键不存在: Key=%s", kv.me, op.Key)
			return ErrNoKey, ""
		}
		
	case "Put":
		kv.kvStore[op.Key] = op.Value
		DPrintf("[Server %d] Put 成功: Key=%s, Value=%s", kv.me, op.Key, op.Value)
		return OK, ""
		
	case "Append":
		kv.kvStore[op.Key] += op.Value
		DPrintf("[Server %d] Append 成功: Key=%s, NewValue=%s", kv.me, op.Key, kv.kvStore[op.Key])
		return OK, ""
		
	default:
		DPrintf("[Server %d] 未知操作类型: %s", kv.me, op.OpType)
		return "ErrUnknownOp", ""
	}
}

// applySnapshot 应用快照到状态机
func (kv *KVServer) applySnapshot(msg raft.ApplyMsg) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	
	// TODO: 实现快照恢复逻辑
	DPrintf("[Server %d] 收到快照: index=%d, term=%d", 
		kv.me, msg.SnapshotIndex, msg.SnapshotTerm)
}

// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should store snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *KVServer {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Op{})

	kv := new(KVServer)
	kv.me = me
	kv.maxraftstate = maxraftstate

	// 初始化KV存储和状态跟踪
	kv.kvStore = make(map[string]string)
	kv.lastApplied = make(map[int64]int64)
	kv.lastResult = make(map[int64]string)
	kv.notifyChans = make(map[int]chan NotifyCh)
	kv.lastAppliedIndex = 0

	kv.applyCh = make(chan raft.ApplyMsg)
	kv.rf = raft.Make(servers, me, persister, kv.applyCh)

	// 启动应用消息处理协程
	go kv.applier()

	DPrintf("[Server %d] KVServer 启动完成", kv.me)
	return kv
}
