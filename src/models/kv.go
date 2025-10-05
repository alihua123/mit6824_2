package models

import "6.5840/porcupine"
import "fmt"
import "sort"

type KvInput struct {
	Op    uint8 // 0 => get, 1 => put, 2 => append
	Key   string
	Value string
}

type KvOutput struct {
	Value string
}

// KvModel 定义了键值存储系统的线性化模型
// 用于Porcupine线性化检查器验证KV操作的正确性
var KvModel = porcupine.Model{
	// Partition 函数：将操作历史按键进行分区
	// 功能：由于不同键之间的操作可以并行执行，按键分区可以提高检查效率
	// 参数：history - 所有操作的历史记录
	// 返回：按键分组的操作序列数组
	Partition: func(history []porcupine.Operation) [][]porcupine.Operation {
		// 创建映射表，按键对操作进行分组
		m := make(map[string][]porcupine.Operation)
		for _, v := range history {
			key := v.Input.(KvInput).Key
			m[key] = append(m[key], v) // 将操作添加到对应键的操作列表中
		}
		// 提取所有键并排序，确保结果的确定性
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys) // 按字典序排序键
		// 按排序后的键顺序构建分区结果
		ret := make([][]porcupine.Operation, 0, len(keys))
		for _, k := range keys {
			ret = append(ret, m[k])
		}
		return ret
	},
	
	// Init 函数：初始化单个键的状态
	// 功能：为每个键设置初始状态（空字符串）
	// 注意：由于按键分区，这里只需要建模单个键的值
	// 返回：键的初始状态（空字符串）
	Init: func() interface{} {
		// 注意：我们在这里建模单个键的值；
		// 由于我们按键进行分区，所以这样做是可以的
		return ""
	},
	
	// Step 函数：执行状态转换并验证操作的正确性
	// 功能：根据输入操作和当前状态，计算新状态并验证输出是否正确
	// 参数：state - 当前状态，input - 输入操作，output - 实际输出
	// 返回：(操作是否正确, 新状态)
	Step: func(state, input, output interface{}) (bool, interface{}) {
		inp := input.(KvInput)   // 类型断言：获取输入操作
		out := output.(KvOutput) // 类型断言：获取输出结果
		st := state.(string)     // 类型断言：获取当前状态（字符串值）
		
		if inp.Op == 0 {
			// Get操作：读取当前值
			// 验证：输出值应该等于当前状态
			// 状态不变：返回原状态
			return out.Value == st, state
		} else if inp.Op == 1 {
			// Put操作：设置新值
			// 验证：Put操作总是成功的
			// 状态更新：新状态为输入的值
			return true, inp.Value
		} else if inp.Op == 2 {
			// Append操作：追加值
			// 验证：Append操作总是成功的
			// 状态更新：新状态为原值加上追加的值
			return true, (st + inp.Value)
		} else {
			// 带返回值的Append操作（扩展操作）
			// 验证：输出值应该等于追加前的原值
			// 状态更新：新状态为原值加上追加的值
			return out.Value == st, (st + inp.Value)
		}
	},
	
	// DescribeOperation 函数：生成操作的可读描述
	// 功能：为调试和可视化提供操作的字符串表示
	// 参数：input - 输入操作，output - 输出结果
	// 返回：操作的描述字符串
	DescribeOperation: func(input, output interface{}) string {
		inp := input.(KvInput)   // 类型断言：获取输入操作
		out := output.(KvOutput) // 类型断言：获取输出结果
		switch inp.Op {
		case 0:
			// Get操作：显示键和返回值
			return fmt.Sprintf("get('%s') -> '%s'", inp.Key, out.Value)
		case 1:
			// Put操作：显示键和设置的值
			return fmt.Sprintf("put('%s', '%s')", inp.Key, inp.Value)
		case 2:
			// Append操作：显示键和追加的值
			return fmt.Sprintf("append('%s', '%s')", inp.Key, inp.Value)
		default:
			// 无效操作
			return "<invalid>"
		}
	},
}
