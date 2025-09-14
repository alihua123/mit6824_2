package kvsrv

// Put或Append操作的参数结构体
// 【关键点】：ClientId和RequestId用于重复检测，防止网络重传导致的重复执行
type PutAppendArgs struct {
	Key   string // 操作的键
	Value string // 操作的值
	// 客户端唯一标识符，用于区分不同客户端
	ClientId int64
	// 请求唯一标识符，用于检测重复请求
	RequestId int64
}

// Put或Append操作的回复结构体
type PutAppendReply struct {
	// 对于Append操作，返回追加前的原始值
	// 对于Put操作，此字段为空
	Value string
}

// Get操作的参数结构体
type GetArgs struct {
	Key string // 要查询的键
	// Get操作通常不需要重复检测，因为它是幂等的
	// 但为了保持一致性，也可以添加客户端标识
	ClientId int64
	RequestId int64
}

// Get操作的回复结构体
type GetReply struct {
	Value string // 键对应的值，如果键不存在则为空字符串
}
