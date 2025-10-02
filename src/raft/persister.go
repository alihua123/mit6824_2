package raft

//
// Raft和kvraft的持久化存储支持
// 用于保存持久化的Raft状态（日志等）和k/v服务器快照
//
// 我们将使用原始的persister.go来测试你的代码进行评分。
// 所以，虽然你可以修改这个代码来帮助调试，但请在提交前使用原始版本进行测试。
//

import "sync"

// Persister 持久化存储器结构体
// 负责安全地存储和读取Raft状态数据和快照数据
type Persister struct {
	mu        sync.Mutex // 互斥锁，保护并发访问
	raftstate []byte     // Raft状态数据（包括当前任期、投票记录、日志条目等）
	snapshot  []byte     // 快照数据（用于日志压缩，存储应用状态的快照）
}

// MakePersister 创建一个新的持久化存储器实例
// 返回初始化后的Persister指针
func MakePersister() *Persister {
	return &Persister{}
}

// clone 创建字节切片的深拷贝
// 参数 orig: 原始字节切片
// 返回: 原始数据的完整副本
// 用于防止外部修改内部存储的数据
func clone(orig []byte) []byte {
	x := make([]byte, len(orig))
	copy(x, orig)
	return x
}

// Copy 创建当前Persister的完整副本
// 返回: 包含相同数据的新Persister实例
// 线程安全：使用互斥锁保护读取操作
func (ps *Persister) Copy() *Persister {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	np := MakePersister()
	np.raftstate = ps.raftstate // 注意：这里是浅拷贝，因为字节切片是不可变的
	np.snapshot = ps.snapshot
	return np
}

// ReadRaftState 读取当前存储的Raft状态数据
// 返回: Raft状态数据的副本（包括term、votedFor、log等）
// 线程安全：使用互斥锁和深拷贝防止数据竞争
func (ps *Persister) ReadRaftState() []byte {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return clone(ps.raftstate)
}

// RaftStateSize 获取当前Raft状态数据的大小
// 返回: Raft状态数据的字节数
// 用于监控存储使用情况和决定是否需要快照
func (ps *Persister) RaftStateSize() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.raftstate)
}

// Save 原子性地保存Raft状态和K/V快照
// 参数 raftstate: 要保存的Raft状态数据
// 参数 snapshot: 要保存的快照数据
// 作为单个原子操作保存，有助于避免它们不同步
// 线程安全：使用互斥锁保护写入操作
func (ps *Persister) Save(raftstate []byte, snapshot []byte) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.raftstate = clone(raftstate) // 深拷贝防止外部修改
	ps.snapshot = clone(snapshot)
}

// ReadSnapshot 读取当前存储的快照数据
// 返回: 快照数据的副本
// 快照包含应用程序状态的压缩表示，用于日志压缩
// 线程安全：使用互斥锁和深拷贝防止数据竞争
func (ps *Persister) ReadSnapshot() []byte {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return clone(ps.snapshot)
}

// SnapshotSize 获取当前快照数据的大小
// 返回: 快照数据的字节数
// 用于监控快照大小和存储使用情况
func (ps *Persister) SnapshotSize() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.snapshot)
}
