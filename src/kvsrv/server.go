package kvsrv

import (
	"log"
	"sync"
)

const Debug = false

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug {
		log.Printf(format, a...)
	}
	return
}

// 用于存储客户端请求历史的结构体
// 【关键点】：防止重复执行同一个请求
type ClientRequest struct {
	RequestId int64 // 请求ID
	Response  string // 该请求的响应结果
}

// KVServer 键值存储服务器
type KVServer struct {
	mu sync.Mutex // 保护并发访问的互斥锁

	// 【核心数据结构】：存储键值对的映射表
	data map[string]string
	
	// 【重复检测机制】：记录每个客户端的最后一次请求
	// key: ClientId, value: 该客户端的最后一次请求信息
	lastRequest map[int64]ClientRequest
}

// Get RPC处理函数
// 【关键点】：Get操作是幂等的，但仍需要加锁保证一致性
func (kv *KVServer) Get(args *GetArgs, reply *GetReply) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	DPrintf("Server: Get key=%s from client=%d", args.Key, args.ClientId)

	// 从数据存储中获取值
	value, exists := kv.data[args.Key]
	if exists {
		reply.Value = value
	} else {
		// 键不存在时返回空字符串
		reply.Value = ""
	}

	DPrintf("Server: Get key=%s, value=%s", args.Key, reply.Value)
}

// Put RPC处理函数
// 【关键点】：Put操作需要重复检测，避免网络重传导致的重复执行
func (kv *KVServer) Put(args *PutAppendArgs, reply *PutAppendReply) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	DPrintf("Server: Put key=%s, value=%s from client=%d, request=%d", 
		args.Key, args.Value, args.ClientId, args.RequestId)

	// 【重复检测】：检查是否为重复请求
	if lastReq, exists := kv.lastRequest[args.ClientId]; exists && lastReq.RequestId == args.RequestId {
		// 重复请求，直接返回之前的结果
		reply.Value = lastReq.Response
		DPrintf("Server: Duplicate Put request detected, returning cached result")
		return
	}

	// 执行Put操作
	kv.data[args.Key] = args.Value
	
	// 记录此次请求，用于重复检测
	kv.lastRequest[args.ClientId] = ClientRequest{
		RequestId: args.RequestId,
		Response:  "", // Put操作不返回值
	}

	reply.Value = ""
	DPrintf("Server: Put completed, key=%s, value=%s", args.Key, args.Value)
}

// Append RPC处理函数
// 【关键点】：Append操作需要返回追加前的原始值，并且需要重复检测
func (kv *KVServer) Append(args *PutAppendArgs, reply *PutAppendReply) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	DPrintf("Server: Append key=%s, value=%s from client=%d, request=%d", 
		args.Key, args.Value, args.ClientId, args.RequestId)

	// 【重复检测】：检查是否为重复请求
	if lastReq, exists := kv.lastRequest[args.ClientId]; exists && lastReq.RequestId == args.RequestId {
		// 重复请求，直接返回之前的结果
		reply.Value = lastReq.Response
		DPrintf("Server: Duplicate Append request detected, returning cached result")
		return
	}

	// 获取当前值（追加前的原始值）
	oldValue := kv.data[args.Key] // 如果键不存在，Go会返回空字符串
	
	// 执行Append操作
	kv.data[args.Key] = oldValue + args.Value
	
	// 记录此次请求，用于重复检测
	kv.lastRequest[args.ClientId] = ClientRequest{
		RequestId: args.RequestId,
		Response:  oldValue, // 返回追加前的原始值
	}

	reply.Value = oldValue
	DPrintf("Server: Append completed, key=%s, old_value=%s, new_value=%s", 
		args.Key, oldValue, kv.data[args.Key])
}

// StartKVServer 启动KV服务器
// 【关键点】：初始化所有必要的数据结构
func StartKVServer() *KVServer {
	kv := new(KVServer)

	// 初始化数据存储
	kv.data = make(map[string]string)
	
	// 初始化客户端请求历史记录
	kv.lastRequest = make(map[int64]ClientRequest)

	DPrintf("Server: KVServer started")
	return kv
}
