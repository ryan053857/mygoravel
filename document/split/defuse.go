package split

import "bytes"

// goldmark 在少数病态输入上不是线性的：
//
//   - 一行开头的容器标记越多，耗时按层数的平方增长；
//   - 缩进越深，每一行检查各层列表项的耗时也越长；
//   - 一个段落开头的每个链接引用定义都会复制段落剩下的行；
//   - 表格中缺少单元格的行会补齐到分隔行的列数，几十 KB 的输入就能分配几 GB。
//
// defuse 在交给 goldmark 的副本上原地改写这些输入，长度不变，所以语法树中的偏移仍然对应原文。
// 正常的文档不受影响；被改写的病态输入只是结构识别得不同，切分的保证照样成立。

const (
	// maxNesting 是一行开头最多识别的容器标记数（引用的 > 和列表标记），再多的部分按文本处理。
	maxNesting = 32
	// maxIndent 是一行开头最多识别的列数，更深的缩进之后按文本处理。
	maxIndent = 128
	// maxReferences 是一个段落中可以是链接引用定义的行数，之后以 [ 开头的行按文本处理。
	maxReferences = 64
)

// defuse 限制每行的嵌套层数和缩进、每个段落的链接引用定义，以及每个表格补齐的单元格数。
func defuse(source []byte) {
	var (
		columns  int // 当前表格的列数，不在表格中时为 0
		size     int // 当前表格已有的字节数
		padded   int // 当前表格已经补齐的单元格数
		run      int // 这一行在连续的非空行中的序号
		previous []byte
	)
	for start := 0; start < len(source); {
		end := bytes.IndexByte(source[start:], '\n')
		if end < 0 {
			end = len(source)
		} else {
			end += start
		}
		line := source[start:end]
		last := len(bytes.TrimRight(line, " \t\r")) // 最后一个非空白字节之后
		blank := last == 0
		var content []byte
		if !blank {
			content = bytes.TrimLeft(line[capNesting(line, last):last], " \t")
		}
		if run >= maxReferences && len(content) > 0 && content[0] == '[' {
			content[0] = 'x'
		}
		switch {
		case blank:
			columns = 0
		case columns > 0:
			// 补齐的单元格最多是表格字节数的一半，与每个单元格都写出来的表格相当。
			size += len(line) + 1
			padded += max(columns-cellCount(content), 0)
			if padded > size/2 {
				// 换成空行结束表格，后面的行按段落处理。
				for i := range line {
					line[i] = ' '
				}
				columns, blank = 0, true
			}
		case run > 0 && isDelimiterRow(content):
			columns, size, padded = cellCount(content), len(previous)+len(line)+2, 0
		}
		if blank {
			run = 0
		} else {
			run++
		}
		previous = line
		start = end + 1
	}
}

// capNesting 返回一行开头的容器标记之后的位置；last 是这一行最后一个非空白字节之后的位置。
// 标记超过 maxNesting 个，或者缩进超过 maxIndent 列时，把那里的字节改成字母，这一行剩下的部分成为文本；
// 从那里开始是分隔线时保持不变，分隔线不会再嵌套。
func capNesting(line []byte, last int) int {
	position, column := 0, 0
	for depth := 0; ; depth++ {
		for position < last && (line[position] == ' ' || line[position] == '\t') {
			if column >= maxIndent {
				line[position] = 'x'
				return position
			}
			if line[position] == '\t' {
				column += 4 - column%4
			} else {
				column++
			}
			position++
		}
		n := markerLength(line[position:])
		if n == 0 {
			return position
		}
		if depth == maxNesting {
			if !isThematicBreakBytes(line[position:]) {
				line[position] = 'x'
			}
			return position
		}
		position, column = position+n, column+n
	}
}

// markerLength 返回 s 开头的引用标记或列表标记的长度，不是标记时返回 0。
func markerLength(s []byte) int {
	if len(s) == 0 {
		return 0
	}
	n := 0
	switch s[0] {
	case '>':
		return 1
	case '-', '*', '+':
		n = 1
	default:
		for n < len(s) && n < 10 && s[n] >= '0' && s[n] <= '9' {
			n++
		}
		if n == 0 || n > 9 || n == len(s) || s[n] != '.' && s[n] != ')' {
			return 0
		}
		n++
	}
	if n < len(s) && s[n] != ' ' && s[n] != '\t' && s[n] != '\r' {
		return 0
	}
	return n
}

// isThematicBreakBytes 报告 s 是否是三个或更多 '*'、'-' 或 '_'，可以夹空白。
func isThematicBreakBytes(s []byte) bool {
	s = bytes.TrimRight(s, " \t\r")
	if len(s) == 0 || s[0] != '*' && s[0] != '-' && s[0] != '_' {
		return false
	}
	count := 0
	for _, b := range s {
		switch b {
		case s[0]:
			count++
		case ' ', '\t':
		default:
			return false
		}
	}
	return count >= 3
}

// isDelimiterRow 宽松地判断 content 是否像表格的分隔行：只含 '-'、'|'、':' 和空白，并且有 '-' 和 '|'。
// 它只用来估算补齐的单元格数，不必与 goldmark 完全一致。
func isDelimiterRow(content []byte) bool {
	if !bytes.Contains(content, []byte("-")) || !bytes.Contains(content, []byte("|")) {
		return false
	}
	for _, b := range content {
		if b != '-' && b != '|' && b != ':' && b != ' ' && b != '\t' {
			return false
		}
	}
	return true
}

// cellCount 估算一行表格的单元格数：未转义的竖线分隔的段数，忽略两侧的竖线。
func cellCount(content []byte) int {
	pipes := 0
	for i, b := range content {
		if b == '|' && (i == 0 || content[i-1] != '\\') {
			pipes++
		}
	}
	if len(content) > 0 && content[0] == '|' {
		pipes--
	}
	if n := len(content); n > 1 && content[n-1] == '|' && content[n-2] != '\\' {
		pipes--
	}
	return max(pipes, 0) + 1
}
