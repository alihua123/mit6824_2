package kvraft

const (
	OK             = "OK"
	ErrNoKey       = "ErrNoKey"       // 键不存在
	ErrWrongLeader = "ErrWrongLeader" // 不是 Leader，客户端需要重试其他服务器
	ErrTimeout     = "ErrTimeout"     // 操作超时
)

type Err string

// Put or Append 操作的参数
type PutAppendArgs struct {
	Key   string
	Value string
	Op    string // "Put" 或 "Append"
	
	// 幂等性保证：客户端唯一标识和请求序列号
	ClientId int64 // 客户端唯一 ID
	SeqNum   int64 // 该客户端的请求序列号（单调递增）
}

type PutAppendReply struct {
	Err Err
}

// Get 操作的参数
type GetArgs struct {
	Key string
	
	// 幂等性保证：客户端唯一标识和请求序列号
	ClientId int64 // 客户端唯一 ID
	SeqNum   int64 // 该客户端的请求序列号（单调递增）
}

type GetReply struct {
	Err   Err
	Value string
}
