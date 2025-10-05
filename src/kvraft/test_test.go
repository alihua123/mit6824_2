package kvraft

import "6.5840/porcupine"
import "6.5840/models"
import "testing"
import "strconv"
import "time"
import "math/rand"
import "strings"
import "sync"
import "sync/atomic"
import "fmt"
import "io/ioutil"

// The tester generously allows solutions to complete elections in one second
// (much more than the paper's range of timeouts).
const electionTimeout = 1 * time.Second

const linearizabilityCheckTimeout = 1 * time.Second

// OpLog 操作日志结构体，用于记录和管理线性一致性检查所需的操作历史
// 该结构体线程安全，可以在多个goroutine中并发使用
type OpLog struct {
	operations []porcupine.Operation // 存储所有操作的切片
	sync.Mutex                       // 保护operations切片的互斥锁
}

// Append 向操作日志中添加一个新操作
// 参数：
//   - op: 要添加的操作，包含输入、输出、时间戳和客户端ID等信息
// 
// 该方法是线程安全的，可以在并发环境中安全调用
func (log *OpLog) Append(op porcupine.Operation) {
	log.Lock()
	defer log.Unlock()
	log.operations = append(log.operations, op)
}

// Read 读取操作日志中的所有操作
// 返回值：
//   - []porcupine.Operation: 所有操作的副本切片
//
// 该方法返回操作的深拷贝，避免外部修改影响内部状态
// 主要用于线性一致性检查时获取完整的操作历史
func (log *OpLog) Read() []porcupine.Operation {
	log.Lock()
	defer log.Unlock()
	ops := make([]porcupine.Operation, len(log.operations))
	copy(ops, log.operations)
	return ops
}

// to make sure timestamps use the monotonic clock, instead of computing
// absolute timestamps with `time.Now().UnixNano()` (which uses the wall
// clock), we measure time relative to `t0` using `time.Since(t0)`, which uses
// the monotonic clock
var t0 = time.Now()

// get/put/putappend that keep counts
func Get(cfg *config, ck *Clerk, key string, log *OpLog, cli int) string {
	start := int64(time.Since(t0))
	v := ck.Get(key)
	end := int64(time.Since(t0))
	cfg.op()
	if log != nil {
		log.Append(porcupine.Operation{
			Input:    models.KvInput{Op: 0, Key: key},
			Output:   models.KvOutput{Value: v},
			Call:     start,
			Return:   end,
			ClientId: cli,
		})
	}

	return v
}

func Put(cfg *config, ck *Clerk, key string, value string, log *OpLog, cli int) {
	start := int64(time.Since(t0))
	ck.Put(key, value)
	end := int64(time.Since(t0))
	cfg.op()
	if log != nil {
		log.Append(porcupine.Operation{
			Input:    models.KvInput{Op: 1, Key: key, Value: value},
			Output:   models.KvOutput{},
			Call:     start,
			Return:   end,
			ClientId: cli,
		})
	}
}

func Append(cfg *config, ck *Clerk, key string, value string, log *OpLog, cli int) {
	start := int64(time.Since(t0))
	ck.Append(key, value)
	end := int64(time.Since(t0))
	cfg.op()
	if log != nil {
		log.Append(porcupine.Operation{
			Input:    models.KvInput{Op: 2, Key: key, Value: value},
			Output:   models.KvOutput{},
			Call:     start,
			Return:   end,
			ClientId: cli,
		})
	}
}

func check(cfg *config, t *testing.T, ck *Clerk, key string, value string) {
	v := Get(cfg, ck, key, nil, -1)
	if v != value {
		t.Fatalf("Get(%v): expected:\n%v\nreceived:\n%v", key, value, v)
	}
}

// a client runs the function f and then signals it is done
func run_client(t *testing.T, cfg *config, me int, ca chan bool, fn func(me int, ck *Clerk, t *testing.T)) {
	ok := false
	defer func() { ca <- ok }()
	ck := cfg.makeClient(cfg.All())
	fn(me, ck, t)
	ok = true
	cfg.deleteClient(ck)
}

// spawn ncli clients and wait until they are all done
func spawn_clients_and_wait(t *testing.T, cfg *config, ncli int, fn func(me int, ck *Clerk, t *testing.T)) {
	ca := make([]chan bool, ncli)
	for cli := 0; cli < ncli; cli++ {
		ca[cli] = make(chan bool)
		go run_client(t, cfg, cli, ca[cli], fn)
	}
	// log.Printf("spawn_clients_and_wait: waiting for clients")
	for cli := 0; cli < ncli; cli++ {
		ok := <-ca[cli]
		// log.Printf("spawn_clients_and_wait: client %d is done\n", cli)
		if ok == false {
			t.Fatalf("failure")
		}
	}
}

// predict effect of Append(k, val) if old value is prev.
func NextValue(prev string, val string) string {
	return prev + val
}

// check that for a specific client all known appends are present in a value,
// and in order
func checkClntAppends(t *testing.T, clnt int, v string, count int) {
	lastoff := -1
	for j := 0; j < count; j++ {
		wanted := "x " + strconv.Itoa(clnt) + " " + strconv.Itoa(j) + " y"
		off := strings.Index(v, wanted)
		if off < 0 {
			t.Fatalf("%v missing element %v in Append result %v", clnt, wanted, v)
		}
		off1 := strings.LastIndex(v, wanted)
		if off1 != off {
			t.Fatalf("duplicate element %v in Append result", wanted)
		}
		if off <= lastoff {
			t.Fatalf("wrong order for element %v in Append result", wanted)
		}
		lastoff = off
	}
}

// check that all known appends are present in a value,
// and are in order for each concurrent client.
func checkConcurrentAppends(t *testing.T, v string, counts []int) {
	nclients := len(counts)
	for i := 0; i < nclients; i++ {
		lastoff := -1
		for j := 0; j < counts[i]; j++ {
			wanted := "x " + strconv.Itoa(i) + " " + strconv.Itoa(j) + " y"
			off := strings.Index(v, wanted)
			if off < 0 {
				t.Fatalf("%v missing element %v in Append result %v", i, wanted, v)
			}
			off1 := strings.LastIndex(v, wanted)
			if off1 != off {
				t.Fatalf("duplicate element %v in Append result", wanted)
			}
			if off <= lastoff {
				t.Fatalf("wrong order for element %v in Append result", wanted)
			}
			lastoff = off
		}
	}
}

// repartition the servers periodically
func partitioner(t *testing.T, cfg *config, ch chan bool, done *int32) {
	defer func() { ch <- true }()
	for atomic.LoadInt32(done) == 0 {
		a := make([]int, cfg.n)
		for i := 0; i < cfg.n; i++ {
			a[i] = (rand.Int() % 2)
		}
		pa := make([][]int, 2)
		for i := 0; i < 2; i++ {
			pa[i] = make([]int, 0)
			for j := 0; j < cfg.n; j++ {
				if a[j] == i {
					pa[i] = append(pa[i], j)
				}
			}
		}
		cfg.partition(pa[0], pa[1])
		time.Sleep(electionTimeout + time.Duration(rand.Int63()%200)*time.Millisecond)
	}
}

// GenericTest KVRaft通用测试框架
// 
// 功能描述：
// 这是KVRaft实验的核心测试函数，用于在各种故障条件下验证分布式KV服务的正确性。
// 测试会创建多个客户端并发执行Get/Put/Append操作，同时模拟各种网络故障和服务器崩溃。
//
// 参数说明：
// - part: 测试部分标识（"4A"或"4B"）
// - nclients: 并发客户端数量
// - nservers: 服务器数量
// - unreliable: 是否启用不可靠网络（丢包、延迟）
// - crash: 是否模拟服务器崩溃重启
// - partitions: 是否模拟网络分区
// - maxraftstate: Raft日志大小限制，正数启用快照，-1禁用快照
// - randomkeys: 是否使用随机键（增加并发冲突）
//
// 测试流程：
// 1. 构建测试标题并初始化配置
// 2. 执行3轮测试迭代，每轮包含：
//    - 启动多个客户端并发执行操作
//    - 根据参数启用网络分区器
//    - 运行5秒后停止客户端和分区器
//    - 如果启用crash，关闭并重启所有服务器
//    - 验证每个客户端的操作结果
//    - 检查快照大小限制
// 3. 使用Porcupine检查操作的线性一致性
//
// 验证内容：
// - 操作的线性一致性（所有操作看起来是原子的、顺序的）
// - 在网络分区下的可用性和一致性权衡
// - 服务器崩溃重启后的状态恢复
// - 快照机制的正确性和大小控制
func GenericTest(t *testing.T, part string, nclients int, nservers int, unreliable bool, crash bool, partitions bool, maxraftstate int, randomkeys bool) {

	// 构建测试标题，根据不同的测试条件组合生成描述性标题
	title := "Test: "
	if unreliable {
		// 网络会丢弃RPC请求和回复
		title = title + "unreliable net, "
	}
	if crash {
		// 节点会重启，因此持久化必须正常工作
		title = title + "restarts, "
	}
	if partitions {
		// 网络可能会分区
		title = title + "partitions, "
	}
	if maxraftstate != -1 {
		// 启用快照功能
		title = title + "snapshots, "
	}
	if randomkeys {
		// 使用随机键进行测试
		title = title + "random keys, "
	}
	if nclients > 1 {
		title = title + "many clients"
	} else {
		title = title + "one client"
	}
	title = title + " (" + part + ")" // 4A or 4B

	// 创建测试配置，包括服务器数量、网络可靠性、快照大小限制
	cfg := make_config(t, nservers, unreliable, maxraftstate)
	defer cfg.cleanup() // 确保测试结束后清理资源

	cfg.begin(title)
	opLog := &OpLog{} // 创建操作日志，用于记录所有客户端操作以进行线性化检查

	ck := cfg.makeClient(cfg.All()) // 创建一个连接到所有服务器的客户端

	// 初始化同步变量和通道
	done_partitioner := int32(0) // 分区器完成标志
	done_clients := int32(0)     // 客户端完成标志
	ch_partitioner := make(chan bool) // 分区器通信通道
	clnts := make([]chan int, nclients) // 每个客户端的结果通道
	for i := 0; i < nclients; i++ {
		clnts[i] = make(chan int)
	}
	
	// 进行3轮测试，每轮都会测试不同的故障场景
	for i := 0; i < 3; i++ {
		// log.Printf("Iteration %v\n", i)
		// 重置同步标志
		atomic.StoreInt32(&done_clients, 0)
		atomic.StoreInt32(&done_partitioner, 0)
		
		// 启动客户端并发执行操作
		go spawn_clients_and_wait(t, cfg, nclients, func(cli int, myck *Clerk, t *testing.T) {
			j := 0 // 操作计数器
			defer func() {
				clnts[cli] <- j // 将操作数量发送到通道
			}()
			last := "" // 仅在非随机键模式下使用，记录最后的值
			if !randomkeys {
				// 非随机键模式：为每个客户端初始化一个键
				Put(cfg, myck, strconv.Itoa(cli), last, opLog, cli)
			}
			// 客户端持续执行操作直到收到停止信号
			for atomic.LoadInt32(&done_clients) == 0 {
				var key string
				if randomkeys {
					// 随机键模式：从0到nclients-1中随机选择键
					key = strconv.Itoa(rand.Intn(nclients))
				} else {
					// 固定键模式：每个客户端使用自己的ID作为键
					key = strconv.Itoa(cli)
				}
				nv := "x " + strconv.Itoa(cli) + " " + strconv.Itoa(j) + " y" // 构造新值
				if (rand.Int() % 1000) < 500 {
					// 50%概率执行Append操作
					// log.Printf("%d: client new append %v\n", cli, nv)
					Append(cfg, myck, key, nv, opLog, cli)
					if !randomkeys {
						last = NextValue(last, nv) // 更新期望值
					}
					j++
				} else if randomkeys && (rand.Int()%1000) < 100 {
					// 随机键模式下10%概率执行Put操作
					// 只在随机键模式下执行，因为在固定键模式下会破坏Get操作后的检查
					Put(cfg, myck, key, nv, opLog, cli)
					j++
				} else {
					// 其余情况执行Get操作
					// log.Printf("%d: client new get %v\n", cli, key)
					v := Get(cfg, myck, key, opLog, cli)
					// 以下检查仅在非随机键模式下有意义
					if !randomkeys && v != last {
						t.Fatalf("get wrong value, key %v, wanted:\n%v\n, got\n%v\n", key, last, v)
					}
				}
			}
		})

		if partitions {
			// 允许客户端在不受干扰的情况下执行一些操作
			time.Sleep(1 * time.Second)
			// 启动网络分区器，模拟网络分区故障
			go partitioner(t, cfg, ch_partitioner, &done_partitioner)
		}
		// 让客户端运行5秒钟
		time.Sleep(5 * time.Second)

		// 通知所有goroutine停止
		atomic.StoreInt32(&done_clients, 1)     // 告诉客户端退出
		atomic.StoreInt32(&done_partitioner, 1) // 告诉分区器退出

		if partitions {
			// log.Printf("wait for partitioner\n")
			<-ch_partitioner // 等待分区器完成
			// 重新连接网络并提交请求。客户端可能已经在少数派中提交了请求。
			// 该请求不会返回，直到该服务器发现新的任期已经开始。
			cfg.ConnectAll()
			// 等待一段时间以便有新的任期
			time.Sleep(electionTimeout)
		}

		if crash {
			// log.Printf("shutdown servers\n")
			// 关闭所有服务器以模拟崩溃
			for i := 0; i < nservers; i++ {
				cfg.ShutdownServer(i)
			}
			// 等待一段时间让服务器关闭，因为关闭不是真正的崩溃，不是瞬时的
			time.Sleep(electionTimeout)
			// log.Printf("restart servers\n")
			// 崩溃并重启所有服务器
			for i := 0; i < nservers; i++ {
				cfg.StartServer(i)
			}
			cfg.ConnectAll() // 重新连接所有服务器
		}

		// log.Printf("wait for clients\n")
		// 等待所有客户端完成并收集结果
		for i := 0; i < nclients; i++ {
			// log.Printf("read from clients %d\n", i)
			j := <-clnts[i] // 接收客户端执行的操作数量
			// if j < 10 {
			// 	log.Printf("Warning: client %d managed to perform only %d put operations in 1 sec?\n", i, j)
			// }
			key := strconv.Itoa(i) // 客户端i对应的键
			// log.Printf("Check %v for client %d\n", j, i)
			// 验证客户端操作的正确性：获取最终值并检查
			v := Get(cfg, ck, key, opLog, 0)
			if !randomkeys {
				// 在非随机键模式下，检查客户端的append操作是否正确
				checkClntAppends(t, i, v, j)
			}
		}

		// 快照相关检查
		if maxraftstate > 0 {
			// 在服务器处理完所有客户端请求并有时间进行检查点后，检查最大值
			sz := cfg.LogSize()
			if sz > 8*maxraftstate {
				t.Fatalf("logs were not trimmed (%v > 8*%v)", sz, maxraftstate)
			}
		}
		if maxraftstate < 0 {
			// 检查是否未使用快照
			ssz := cfg.SnapshotSize()
			if ssz > 0 {
				t.Fatalf("snapshot too large (%v), should not be used when maxraftstate = %d", ssz, maxraftstate)
			}
		}
	}

	// 线性化检查：验证所有操作的执行顺序是否符合线性化要求
	res, info := porcupine.CheckOperationsVerbose(models.KvModel, opLog.Read(), linearizabilityCheckTimeout)
	if res == porcupine.Illegal {
		// 如果检测到非线性化行为，生成可视化文件帮助调试
		file, err := ioutil.TempFile("", "*.html")
		if err != nil {
			fmt.Printf("info: failed to create temp file for visualization")
		} else {
			err = porcupine.Visualize(models.KvModel, info, file)
			if err != nil {
				fmt.Printf("info: failed to write history visualization to %s\n", file.Name())
			} else {
				fmt.Printf("info: wrote history visualization to %s\n", file.Name())
			}
		}
		t.Fatal("history is not linearizable")
	} else if res == porcupine.Unknown {
		// 线性化检查超时，假设历史记录是正确的
		fmt.Println("info: linearizability check timed out, assuming history is ok")
	}

	cfg.end() // 结束测试配置
}

// GenericTestSpeed KVRaft性能测试框架
//
// 功能描述：
// 测试KV服务在正常条件下的操作完成速度，确保系统性能满足要求。
// 要求操作完成速度优于心跳间隔，即每个心跳间隔内至少完成3个操作。
//
// 参数说明：
// - part: 测试部分标识（"4A"或"4B"）
// - maxraftstate: Raft日志大小限制，用于快照测试
//
// 测试流程：
// 1. 创建3个服务器的集群，网络可靠
// 2. 等待第一个操作完成，确保leader选举完成
// 3. 连续执行1000个Append操作并计时
// 4. 验证所有操作都正确完成
// 5. 检查平均操作时间是否满足性能要求
//
// 性能要求：
// - 心跳间隔约100ms，要求每个间隔至少完成3个操作
// - 即每个操作应在33.33ms内完成
// - 如果操作过慢会导致测试失败
func GenericTestSpeed(t *testing.T, part string, maxraftstate int) {
	const nservers = 3
	const numOps = 1000
	cfg := make_config(t, nservers, false, maxraftstate)
	defer cfg.cleanup()

	ck := cfg.makeClient(cfg.All())

	cfg.begin(fmt.Sprintf("Test: ops complete fast enough (%s)", part))

	// wait until first op completes, so we know a leader is elected
	// and KV servers are ready to process client requests
	ck.Get("x")

	start := time.Now()
	for i := 0; i < numOps; i++ {
		ck.Append("x", "x 0 "+strconv.Itoa(i)+" y")
	}
	dur := time.Since(start)

	v := ck.Get("x")
	checkClntAppends(t, 0, v, numOps)

	// heartbeat interval should be ~ 100 ms; require at least 3 ops per
	const heartbeatInterval = 100 * time.Millisecond
	const opsPerInterval = 3
	const timePerOp = heartbeatInterval / opsPerInterval
	if dur > numOps*timePerOp {
		t.Fatalf("Operations completed too slowly %v/op > %v/op\n", dur/numOps, timePerOp)
	}

	cfg.end()
}

// TestBasic4A 测试基本的KV操作功能
// 测试目标：验证单个客户端在稳定网络环境下的基本Get/Put/Append操作
// 测试条件：1个客户端，5个服务器，网络可靠，无崩溃，无分区，无快照
func TestBasic4A(t *testing.T) {
	// Test: one client (4A) ...
	GenericTest(t, "4A", 1, 5, false, false, false, -1, false)
}

// TestSpeed4A 测试操作完成速度
// 测试目标：验证操作能够快速完成，性能要求至少每个心跳间隔完成3个操作
// 测试条件：执行1000个Append操作，检查总耗时是否满足性能要求
func TestSpeed4A(t *testing.T) {
	GenericTestSpeed(t, "4A", -1)
}

// TestConcurrent4A 测试多客户端并发操作
// 测试目标：验证多个客户端同时进行KV操作时的正确性和一致性
// 测试条件：5个客户端，5个服务器，网络可靠，无崩溃，无分区，无快照
func TestConcurrent4A(t *testing.T) {
	// Test: many clients (4A) ...
	GenericTest(t, "4A", 5, 5, false, false, false, -1, false)
}

// TestUnreliable4A 测试不可靠网络环境下的多客户端操作
// 测试目标：验证在网络丢包、延迟等不可靠条件下系统的容错能力
// 测试条件：5个客户端，5个服务器，网络不可靠，无崩溃，无分区，无快照
func TestUnreliable4A(t *testing.T) {
	// Test: unreliable net, many clients (4A) ...
	GenericTest(t, "4A", 5, 5, true, false, false, -1, false)
}

// TestUnreliableOneKey4A 测试不可靠网络下对同一键的并发Append操作
// 测试目标：验证多个客户端对同一个键进行并发Append时的操作顺序和完整性
// 测试条件：5个客户端对同一键"k"进行Append，网络不可靠，检查所有操作都被正确应用且顺序正确
func TestUnreliableOneKey4A(t *testing.T) {
	const nservers = 3
	cfg := make_config(t, nservers, true, -1)
	defer cfg.cleanup()

	ck := cfg.makeClient(cfg.All())

	cfg.begin("Test: concurrent append to same key, unreliable (4A)")

	Put(cfg, ck, "k", "", nil, -1)

	const nclient = 5
	const upto = 10
	spawn_clients_and_wait(t, cfg, nclient, func(me int, myck *Clerk, t *testing.T) {
		n := 0
		for n < upto {
			Append(cfg, myck, "k", "x "+strconv.Itoa(me)+" "+strconv.Itoa(n)+" y", nil, -1)
			n++
		}
	})

	var counts []int
	for i := 0; i < nclient; i++ {
		counts = append(counts, upto)
	}

	vx := Get(cfg, ck, "k", nil, -1)
	checkConcurrentAppends(t, vx, counts)

	cfg.end()
}

// TestOnePartition4A 测试网络分区场景
// 测试目标：验证在网络分区情况下，多数派能继续工作，少数派会阻塞，分区恢复后操作能正常完成
// 测试场景：
// 1. 将5个服务器分成两个分区，多数派(3个)继续提供服务
// 2. 少数派(2个)的操作应该被阻塞
// 3. 分区恢复后，被阻塞的操作应该能够完成
func TestOnePartition4A(t *testing.T) {
	const nservers = 5
	cfg := make_config(t, nservers, false, -1)
	defer cfg.cleanup()
	ck := cfg.makeClient(cfg.All())

	Put(cfg, ck, "1", "13", nil, -1)

	cfg.begin("Test: progress in majority (4A)")

	p1, p2 := cfg.make_partition()
	cfg.partition(p1, p2)

	ckp1 := cfg.makeClient(p1)  // connect ckp1 to p1
	ckp2a := cfg.makeClient(p2) // connect ckp2a to p2
	ckp2b := cfg.makeClient(p2) // connect ckp2b to p2

	Put(cfg, ckp1, "1", "14", nil, -1)
	check(cfg, t, ckp1, "1", "14")

	cfg.end()

	done0 := make(chan bool)
	done1 := make(chan bool)

	cfg.begin("Test: no progress in minority (4A)")
	go func() {
		Put(cfg, ckp2a, "1", "15", nil, -1)
		done0 <- true
	}()
	go func() {
		Get(cfg, ckp2b, "1", nil, -1) // different clerk in p2
		done1 <- true
	}()

	select {
	case <-done0:
		t.Fatalf("Put in minority completed")
	case <-done1:
		t.Fatalf("Get in minority completed")
	case <-time.After(time.Second):
	}

	check(cfg, t, ckp1, "1", "14")
	Put(cfg, ckp1, "1", "16", nil, -1)
	check(cfg, t, ckp1, "1", "16")

	cfg.end()

	cfg.begin("Test: completion after heal (4A)")

	cfg.ConnectAll()
	cfg.ConnectClient(ckp2a, cfg.All())
	cfg.ConnectClient(ckp2b, cfg.All())

	time.Sleep(electionTimeout)

	select {
	case <-done0:
	case <-time.After(30 * 100 * time.Millisecond):
		t.Fatalf("Put did not complete")
	}

	select {
	case <-done1:
	case <-time.After(30 * 100 * time.Millisecond):
		t.Fatalf("Get did not complete")
	default:
	}

	check(cfg, t, ck, "1", "15")

	cfg.end()
}

// TestManyPartitionsOneClient4A 测试频繁网络分区下的单客户端操作
// 测试目标：验证在网络频繁分区重组的情况下，单个客户端的操作仍能保持一致性
// 测试条件：1个客户端，5个服务器，无崩溃，频繁分区，无快照
func TestManyPartitionsOneClient4A(t *testing.T) {
	// Test: partitions, one client (4A) ...
	GenericTest(t, "4A", 1, 5, false, false, true, -1, false)
}

// TestManyPartitionsManyClients4A 测试频繁网络分区下的多客户端操作
// 测试目标：验证在网络频繁分区重组的情况下，多个客户端的并发操作仍能保持一致性
// 测试条件：5个客户端，5个服务器，无崩溃，频繁分区，无快照
func TestManyPartitionsManyClients4A(t *testing.T) {
	// Test: partitions, many clients (4A) ...
	GenericTest(t, "4A", 5, 5, false, false, true, -1, false)
}

// TestPersistOneClient4A 测试服务器重启后的持久化恢复（单客户端）
// 测试目标：验证服务器崩溃重启后能正确恢复状态，单客户端操作保持一致性
// 测试条件：1个客户端，5个服务器，网络可靠，有崩溃重启，无分区，无快照
func TestPersistOneClient4A(t *testing.T) {
	// Test: restarts, one client (4A) ...
	GenericTest(t, "4A", 1, 5, false, true, false, -1, false)
}

// TestPersistConcurrent4A 测试服务器重启后的持久化恢复（多客户端）
// 测试目标：验证服务器崩溃重启后能正确恢复状态，多客户端并发操作保持一致性
// 测试条件：5个客户端，5个服务器，网络可靠，有崩溃重启，无分区，无快照
func TestPersistConcurrent4A(t *testing.T) {
	// Test: restarts, many clients (4A) ...
	GenericTest(t, "4A", 5, 5, false, true, false, -1, false)
}

// TestPersistConcurrentUnreliable4A 测试不可靠网络+服务器重启的综合场景
// 测试目标：验证在网络不可靠且服务器会崩溃重启的复杂环境下系统的健壮性
// 测试条件：5个客户端，5个服务器，网络不可靠，有崩溃重启，无分区，无快照
func TestPersistConcurrentUnreliable4A(t *testing.T) {
	// Test: unreliable net, restarts, many clients (4A) ...
	GenericTest(t, "4A", 5, 5, true, true, false, -1, false)
}

// TestPersistPartition4A 测试服务器重启+网络分区的综合场景
// 测试目标：验证在服务器会崩溃重启且网络会分区的复杂环境下系统的健壮性
// 测试条件：5个客户端，5个服务器，网络可靠，有崩溃重启，有分区，无快照
func TestPersistPartition4A(t *testing.T) {
	// Test: restarts, partitions, many clients (4A) ...
	GenericTest(t, "4A", 5, 5, false, true, true, -1, false)
}

// TestPersistPartitionUnreliable4A 测试最复杂的4A场景：不可靠网络+重启+分区
// 测试目标：验证在所有可能的故障条件下（网络不可靠、服务器重启、网络分区）系统的健壮性
// 测试条件：5个客户端，5个服务器，网络不可靠，有崩溃重启，有分区，无快照
func TestPersistPartitionUnreliable4A(t *testing.T) {
	// Test: unreliable net, restarts, partitions, many clients (4A) ...
	GenericTest(t, "4A", 5, 5, true, true, true, -1, false)
}

// TestPersistPartitionUnreliableLinearizable4A 测试线性一致性：最复杂场景+随机键
// 测试目标：在最复杂的故障环境下验证系统的线性一致性，使用随机键增加测试复杂度
// 测试条件：15个客户端，7个服务器，网络不可靠，有崩溃重启，有分区，随机键，无快照
func TestPersistPartitionUnreliableLinearizable4A(t *testing.T) {
	// Test: unreliable net, restarts, partitions, random keys, many clients (4A) ...
	GenericTest(t, "4A", 15, 7, true, true, true, -1, true)
}

// TestSnapshotRPC4B 测试InstallSnapshot RPC机制
// 测试目标：验证落后的服务器能通过InstallSnapshot RPC追赶上最新状态
// 测试场景：
// 1. 创建网络分区，让一个服务器落后
// 2. 多数派继续操作并触发快照，丢弃旧日志
// 3. 重新分区让落后服务器参与，验证其能通过InstallSnapshot RPC恢复
func TestSnapshotRPC4B(t *testing.T) {
	const nservers = 3
	maxraftstate := 1000
	cfg := make_config(t, nservers, false, maxraftstate)
	defer cfg.cleanup()

	ck := cfg.makeClient(cfg.All())

	cfg.begin("Test: InstallSnapshot RPC (4B)")

	Put(cfg, ck, "a", "A", nil, -1)
	check(cfg, t, ck, "a", "A")

	// a bunch of puts into the majority partition.
	cfg.partition([]int{0, 1}, []int{2})
	{
		ck1 := cfg.makeClient([]int{0, 1})
		for i := 0; i < 50; i++ {
			Put(cfg, ck1, strconv.Itoa(i), strconv.Itoa(i), nil, -1)
		}
		time.Sleep(electionTimeout)
		Put(cfg, ck1, "b", "B", nil, -1)
	}

	// check that the majority partition has thrown away
	// most of its log entries.
	sz := cfg.LogSize()
	if sz > 8*maxraftstate {
		t.Fatalf("logs were not trimmed (%v > 8*%v)", sz, maxraftstate)
	}

	// now make group that requires participation of
	// lagging server, so that it has to catch up.
	cfg.partition([]int{0, 2}, []int{1})
	{
		ck1 := cfg.makeClient([]int{0, 2})
		Put(cfg, ck1, "c", "C", nil, -1)
		Put(cfg, ck1, "d", "D", nil, -1)
		check(cfg, t, ck1, "a", "A")
		check(cfg, t, ck1, "b", "B")
		check(cfg, t, ck1, "1", "1")
		check(cfg, t, ck1, "49", "49")
	}

	// now everybody
	cfg.partition([]int{0, 1, 2}, []int{})

	Put(cfg, ck, "e", "E", nil, -1)
	check(cfg, t, ck, "c", "C")
	check(cfg, t, ck, "e", "E")
	check(cfg, t, ck, "1", "1")

	cfg.end()
}

// TestSnapshotSize4B 测试快照大小的合理性
// 测试目标：验证快照大小在合理范围内，不会过大占用过多内存
// 测试条件：执行大量操作后检查快照大小是否控制在500字节以内
func TestSnapshotSize4B(t *testing.T) {
	const nservers = 3
	maxraftstate := 1000
	maxsnapshotstate := 500
	cfg := make_config(t, nservers, false, maxraftstate)
	defer cfg.cleanup()

	ck := cfg.makeClient(cfg.All())

	cfg.begin("Test: snapshot size is reasonable (4B)")

	for i := 0; i < 200; i++ {
		Put(cfg, ck, "x", "0", nil, -1)
		check(cfg, t, ck, "x", "0")
		Put(cfg, ck, "x", "1", nil, -1)
		check(cfg, t, ck, "x", "1")
	}

	// check that servers have thrown away most of their log entries
	sz := cfg.LogSize()
	if sz > 8*maxraftstate {
		t.Fatalf("logs were not trimmed (%v > 8*%v)", sz, maxraftstate)
	}

	// check that the snapshots are not unreasonably large
	ssz := cfg.SnapshotSize()
	if ssz > maxsnapshotstate {
		t.Fatalf("snapshot too large (%v > %v)", ssz, maxsnapshotstate)
	}

	cfg.end()
}

// TestSpeed4B 测试启用快照后的操作速度
// 测试目标：验证启用快照机制后系统仍能保持良好的性能表现
// 测试条件：执行1000个操作，检查完成速度是否满足性能要求
func TestSpeed4B(t *testing.T) {
	GenericTestSpeed(t, "4B", 1000)
}

// TestSnapshotRecover4B 测试快照恢复功能（单客户端）
// 测试目标：验证服务器重启后能从快照正确恢复状态
// 测试条件：1个客户端，5个服务器，网络可靠，有崩溃重启，无分区，启用快照（maxraftstate=1000）
func TestSnapshotRecover4B(t *testing.T) {
	// Test: restarts, snapshots, one client (4B) ...
	GenericTest(t, "4B", 1, 5, false, true, false, 1000, false)
}

// TestSnapshotRecoverManyClients4B 测试多客户端场景下的快照恢复
// 测试目标：验证在多客户端并发操作下，服务器重启后能从快照正确恢复状态
// 测试条件：20个客户端，5个服务器，网络可靠，有崩溃重启，无分区，启用快照（maxraftstate=1000）
func TestSnapshotRecoverManyClients4B(t *testing.T) {
	// Test: restarts, snapshots, many clients (4B) ...
	GenericTest(t, "4B", 20, 5, false, true, false, 1000, false)
}

// TestSnapshotUnreliable4B 测试不可靠网络下的快照功能
// 测试目标：验证在网络不可靠的环境下快照机制仍能正常工作
// 测试条件：5个客户端，5个服务器，网络不可靠，无崩溃，无分区，启用快照（maxraftstate=1000）
func TestSnapshotUnreliable4B(t *testing.T) {
	// Test: unreliable net, snapshots, many clients (4B) ...
	GenericTest(t, "4B", 5, 5, true, false, false, 1000, false)
}

// TestSnapshotUnreliableRecover4B 测试不可靠网络+重启的快照恢复
// 测试目标：验证在网络不可靠且服务器会重启的复杂环境下快照恢复的正确性
// 测试条件：5个客户端，5个服务器，网络不可靠，有崩溃重启，无分区，启用快照（maxraftstate=1000）
func TestSnapshotUnreliableRecover4B(t *testing.T) {
	// Test: unreliable net, restarts, snapshots, many clients (4B) ...
	GenericTest(t, "4B", 5, 5, true, true, false, 1000, false)
}

// TestSnapshotUnreliableRecoverConcurrentPartition4B 测试最复杂的4B场景
// 测试目标：验证在所有故障条件下（不可靠网络、重启、分区）快照机制的健壮性
// 测试条件：5个客户端，5个服务器，网络不可靠，有崩溃重启，有分区，启用快照（maxraftstate=1000）
func TestSnapshotUnreliableRecoverConcurrentPartition4B(t *testing.T) {
	// Test: unreliable net, restarts, partitions, snapshots, many clients (4B) ...
	GenericTest(t, "4B", 5, 5, true, true, true, 1000, false)
}

// TestSnapshotUnreliableRecoverConcurrentPartitionLinearizable4B 测试线性一致性：最复杂快照场景
// 测试目标：在最复杂的故障环境下验证快照机制的线性一致性，使用随机键增加测试复杂度
// 测试条件：15个客户端，7个服务器，网络不可靠，有崩溃重启，有分区，启用快照（maxraftstate=1000），随机键
func TestSnapshotUnreliableRecoverConcurrentPartitionLinearizable4B(t *testing.T) {
	// Test: unreliable net, restarts, partitions, snapshots, random keys, many clients (4B) ...
	GenericTest(t, "4B", 15, 7, true, true, true, 1000, true)
}
