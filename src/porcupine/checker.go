package porcupine

import (
	"sort"
	"sync/atomic"
	"time"
)

// entryKind 表示条目类型：调用或返回
type entryKind bool

const (
	callEntry   entryKind = false // 调用条目
	returnEntry           = true  // 返回条目
)

// entry 表示操作历史中的一个条目（调用或返回）
type entry struct {
	kind     entryKind   // 条目类型（调用或返回）
	value    interface{} // 操作的输入或输出值
	id       int         // 操作的唯一标识符
	time     int64       // 操作发生的时间戳
	clientId int         // 执行操作的客户端ID
}

// linearizationInfo 包含线性化检查的详细信息，用于调试和可视化
type linearizationInfo struct {
	history               [][]entry // 每个分区的条目列表
	partialLinearizations [][][]int // 每个分区的部分线性化序列集合
}

type byTime []entry

func (a byTime) Len() int {
	return len(a)
}

func (a byTime) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func (a byTime) Less(i, j int) bool {
	if a[i].time != a[j].time {
		return a[i].time < a[j].time
	}
	// if the timestamps are the same, we need to make sure we order calls
	// before returns
	return a[i].kind == callEntry && a[j].kind == returnEntry
}

func makeEntries(history []Operation) []entry {
	var entries []entry = nil
	id := 0
	for _, elem := range history {
		entries = append(entries, entry{
			callEntry, elem.Input, id, elem.Call, elem.ClientId})
		entries = append(entries, entry{
			returnEntry, elem.Output, id, elem.Return, elem.ClientId})
		id++
	}
	sort.Sort(byTime(entries))
	return entries
}

type node struct {
	value interface{}
	match *node // call if match is nil, otherwise return
	id    int
	next  *node
	prev  *node
}

func insertBefore(n *node, mark *node) *node {
	if mark != nil {
		beforeMark := mark.prev
		mark.prev = n
		n.next = mark
		if beforeMark != nil {
			n.prev = beforeMark
			beforeMark.next = n
		}
	}
	return n
}

func length(n *node) int {
	l := 0
	for n != nil {
		n = n.next
		l++
	}
	return l
}

func renumber(events []Event) []Event {
	var e []Event
	m := make(map[int]int) // renumbering
	id := 0
	for _, v := range events {
		if r, ok := m[v.Id]; ok {
			e = append(e, Event{v.ClientId, v.Kind, v.Value, r})
		} else {
			e = append(e, Event{v.ClientId, v.Kind, v.Value, id})
			m[v.Id] = id
			id++
		}
	}
	return e
}

func convertEntries(events []Event) []entry {
	var entries []entry
	for i, elem := range events {
		kind := callEntry
		if elem.Kind == ReturnEvent {
			kind = returnEntry
		}
		// use index as "time"
		entries = append(entries, entry{kind, elem.Value, elem.Id, int64(i), elem.ClientId})
	}
	return entries
}

func makeLinkedEntries(entries []entry) *node {
	var root *node = nil
	match := make(map[int]*node)
	for i := len(entries) - 1; i >= 0; i-- {
		elem := entries[i]
		if elem.kind == returnEntry {
			entry := &node{value: elem.value, match: nil, id: elem.id}
			match[elem.id] = entry
			insertBefore(entry, root)
			root = entry
		} else {
			entry := &node{value: elem.value, match: match[elem.id], id: elem.id}
			insertBefore(entry, root)
			root = entry
		}
	}
	return root
}

type cacheEntry struct {
	linearized bitset
	state      interface{}
}

func cacheContains(model Model, cache map[uint64][]cacheEntry, entry cacheEntry) bool {
	for _, elem := range cache[entry.linearized.hash()] {
		if entry.linearized.equals(elem.linearized) && model.Equal(entry.state, elem.state) {
			return true
		}
	}
	return false
}

type callsEntry struct {
	entry *node
	state interface{}
}

func lift(entry *node) {
	entry.prev.next = entry.next
	entry.next.prev = entry.prev
	match := entry.match
	match.prev.next = match.next
	if match.next != nil {
		match.next.prev = match.prev
	}
}

func unlift(entry *node) {
	match := entry.match
	match.prev.next = match
	if match.next != nil {
		match.next.prev = match
	}
	entry.prev.next = entry
	entry.next.prev = entry
}

// checkSingle 对单个分区的操作历史进行线性一致性检查
// 这是Porcupine算法的核心实现，使用深度优先搜索来寻找有效的线性化序列
// 参数：
//   - model: 系统模型，定义状态转换规则
//   - history: 单个分区的操作历史条目列表
//   - computePartial: 是否计算部分线性化信息（用于调试）
//   - kill: 用于提前终止检查的原子标志
// 返回值：
//   - bool: 该分区是否满足线性一致性
//   - []*[]int: 包含每个操作的最长线性化前缀（用于调试）
//
// 算法原理：
// 1. 将操作历史转换为链表结构，每个调用操作与其对应的返回操作配对
// 2. 使用深度优先搜索遍历所有可能的操作执行顺序
// 3. 对于每个可能的操作，检查其是否能在当前状态下合法执行
// 4. 使用缓存避免重复计算相同的状态组合
// 5. 通过回溯机制探索所有可能的线性化序列
func checkSingle(model Model, history []entry, computePartial bool, kill *int32) (bool, []*[]int) {
	entry := makeLinkedEntries(history)                    // 将条目转换为链表结构
	n := length(entry) / 2                                 // 操作数量（每个操作有调用和返回两个条目）
	linearized := newBitset(uint(n))                       // 记录已线性化的操作
	cache := make(map[uint64][]cacheEntry)                 // 缓存：从状态哈希到缓存条目的映射
	var calls []callsEntry                                 // 当前线性化序列中的调用栈
	longest := make([]*[]int, n)                           // 每个操作的最长线性化前缀

	state := model.Init()                                  // 初始化系统状态
	headEntry := insertBefore(&node{value: nil, match: nil, id: -1}, entry) // 创建头节点
	
	// 深度优先搜索遍历所有可能的操作执行顺序
	for headEntry.next != nil {
		// 检查是否需要提前终止
		if atomic.LoadInt32(kill) != 0 {
			return false, longest
		}
		
		// 当前条目是调用操作且有匹配的返回操作
		if entry.match != nil {
			matching := entry.match // 对应的返回条目
			// 检查该操作是否能在当前状态下合法执行
			ok, newState := model.Step(state, entry.value, matching.value)
			if ok {
				// 操作合法，尝试将其加入线性化序列
				newLinearized := linearized.clone().set(uint(entry.id))
				newCacheEntry := cacheEntry{newLinearized, newState}
				
				// 检查缓存中是否已存在相同的状态组合
				if !cacheContains(model, cache, newCacheEntry) {
					// 新状态，加入缓存并继续搜索
					hash := newLinearized.hash()
					cache[hash] = append(cache[hash], newCacheEntry)
					calls = append(calls, callsEntry{entry, state})
					state = newState
					linearized.set(uint(entry.id))
					lift(entry)                    // 将操作从链表中移除
					entry = headEntry.next         // 继续处理下一个操作
				} else {
					// 状态已存在于缓存中，跳过该操作
					entry = entry.next
				}
			} else {
				// 操作不合法，跳过该操作
				entry = entry.next
			}
		} else {
			// 当前条目是返回操作或无匹配的调用操作，需要回溯
			if len(calls) == 0 {
				// 无法回溯，线性化失败
				return false, longest
			}
			
			// 计算部分线性化信息（用于调试）
			if computePartial {
				callsLen := len(calls)
				var seq []int = nil
				for _, v := range calls {
					if longest[v.entry.id] == nil || callsLen > len(*longest[v.entry.id]) {
						// 延迟创建序列
						if seq == nil {
							seq = make([]int, len(calls))
							for i, v := range calls {
								seq[i] = v.entry.id
							}
						}
						longest[v.entry.id] = &seq
					}
				}
			}
			
			// 回溯到上一个状态
			callsTop := calls[len(calls)-1]
			entry = callsTop.entry
			state = callsTop.state
			linearized.clear(uint(entry.id))
			calls = calls[:len(calls)-1]
			unlift(entry)                      // 将操作重新加入链表
			entry = entry.next                 // 尝试下一个可能的操作
		}
	}
	
	// 找到完整的线性化序列
	seq := make([]int, len(calls))
	for i, v := range calls {
		seq[i] = v.entry.id
	}
	for i := 0; i < n; i++ {
		longest[i] = &seq
	}
	return true, longest
}

func fillDefault(model Model) Model {
	if model.Partition == nil {
		model.Partition = NoPartition
	}
	if model.PartitionEvent == nil {
		model.PartitionEvent = NoPartitionEvent
	}
	if model.Equal == nil {
		model.Equal = ShallowEqual
	}
	if model.DescribeOperation == nil {
		model.DescribeOperation = DefaultDescribeOperation
	}
	if model.DescribeState == nil {
		model.DescribeState = DefaultDescribeState
	}
	return model
}

// checkParallel 并行检查多个分区的线性一致性
// 参数：
//   - model: 系统模型
//   - history: 按分区组织的操作历史，每个分区独立检查
//   - computeInfo: 是否计算详细的线性化信息用于调试
//   - timeout: 检查超时时间，0表示无超时限制
// 返回值：
//   - CheckResult: 检查结果（Ok、Illegal或Unknown）
//   - linearizationInfo: 详细的线性化信息（仅在computeInfo为true时有效）
//
// 该函数为每个分区启动一个goroutine进行并行检查，提高检查效率
func checkParallel(model Model, history [][]entry, computeInfo bool, timeout time.Duration) (CheckResult, linearizationInfo) {
	ok := true
	timedOut := false
	results := make(chan bool, len(history))        // 收集各分区检查结果的通道
	longest := make([][]*[]int, len(history))       // 存储各分区的最长线性化前缀
	kill := int32(0)                                // 用于提前终止所有goroutine的标志
	
	// 为每个分区启动独立的检查goroutine
	for i, subhistory := range history {
		go func(i int, subhistory []entry) {
			ok, l := checkSingle(model, subhistory, computeInfo, &kill)
			longest[i] = l
			results <- ok
		}(i, subhistory)
	}
	
	// 设置超时机制
	var timeoutChan <-chan time.Time
	if timeout > 0 {
		timeoutChan = time.After(timeout)
	}
	
	count := 0
loop:
	for {
		select {
		case result := <-results:
			count++
			ok = ok && result
			// 如果发现非线性化且不需要详细信息，立即终止其他goroutine
			if !ok && !computeInfo {
				atomic.StoreInt32(&kill, 1)
				break loop
			}
			// 所有分区检查完成
			if count >= len(history) {
				break loop
			}
		case <-timeoutChan:
			// 检查超时，可能产生假阳性结果
			timedOut = true
			atomic.StoreInt32(&kill, 1)
			break loop
		}
	}
	
	var info linearizationInfo
	if computeInfo {
		// 确保所有goroutine都已完成，避免竞态条件
		for count < len(history) {
			<-results
			count++
		}
		
		// 构建部分线性化信息用于调试
		partialLinearizations := make([][][]int, len(history))
		for i := 0; i < len(history); i++ {
			var partials [][]int
			// 将longest转换为唯一线性化序列的集合
			set := make(map[*[]int]struct{})
			for _, v := range longest[i] {
				if v != nil {
					set[v] = struct{}{}
				}
			}
			for k := range set {
				arr := make([]int, len(*k))
				for i, v := range *k {
					arr[i] = v
				}
				partials = append(partials, arr)
			}
			partialLinearizations[i] = partials
		}
		info.history = history
		info.partialLinearizations = partialLinearizations
	}
	
	// 确定最终检查结果
	var result CheckResult
	if !ok {
		result = Illegal    // 发现非线性化行为
	} else {
		if timedOut {
			result = Unknown // 检查超时，结果未知
		} else {
			result = Ok      // 通过线性一致性检查
		}
	}
	return result, info
}

// checkEvents 检查事件历史的线性一致性
// 参数：
//   - model: 系统模型
//   - history: 事件历史记录
//   - verbose: 是否返回详细信息
//   - timeout: 检查超时时间
// 返回值：
//   - CheckResult: 检查结果
//   - linearizationInfo: 详细信息（仅在verbose为true时有效）
func checkEvents(model Model, history []Event, verbose bool, timeout time.Duration) (CheckResult, linearizationInfo) {
	model = fillDefault(model)                          // 填充模型的默认方法
	partitions := model.PartitionEvent(history)         // 按键分区事件历史
	l := make([][]entry, len(partitions))
	for i, subhistory := range partitions {
		l[i] = convertEntries(renumber(subhistory))     // 转换事件为条目格式
	}
	return checkParallel(model, l, verbose, timeout)    // 并行检查各分区
}

// checkOperations 检查操作历史的线性一致性（内部实现）
// 参数：
//   - model: 系统模型
//   - history: 操作历史记录
//   - verbose: 是否返回详细信息
//   - timeout: 检查超时时间
// 返回值：
//   - CheckResult: 检查结果
//   - linearizationInfo: 详细信息（仅在verbose为true时有效）
//
// 这是所有CheckOperations*函数的底层实现
func checkOperations(model Model, history []Operation, verbose bool, timeout time.Duration) (CheckResult, linearizationInfo) {
	model = fillDefault(model)                          // 填充模型的默认方法
	partitions := model.Partition(history)              // 按键分区操作历史
	l := make([][]entry, len(partitions))
	for i, subhistory := range partitions {
		l[i] = makeEntries(subhistory)                  // 转换操作为条目格式
	}
	return checkParallel(model, l, verbose, timeout)    // 并行检查各分区
}
