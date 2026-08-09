package tui

// sprites.go —— 物种精灵帧数据（纯数据，方便以后扩充新物种/新动作）。
//
// 约定：
//   - 每帧是多行字符串；{e} 为眼睛占位（3 字符宽），{m} 为嘴部占位（1 字符），
//     渲染时按当前心情替换（view.go 的 faceFor）。
//   - 帧统一 3-5 行高、≤14 字符宽，纯 ASCII，避免宽度对齐问题。
//   - Idle/Sleep 为循环帧（按帧号取模）；Eat/Play/Clean 为一次性动作帧。

// sprite 是一个物种的全套帧。
type sprite struct {
	Idle  []string // 醒着默认循环（≥2 帧：眨眼/起伏）
	Sleep []string // 睡觉循环（闭眼 + Z 递增）
	Eat   []string // 吃饭（嘴张合 + 食物消失）
	Play  []string // 玩耍（位移/跳跃）
	Clean []string // 清洁（泡泡环绕）
	Dead  string   // RIP 画面
}

// spriteFor 按物种取帧集；未知物种回落到 blob。
func spriteFor(species string) *sprite {
	if s, ok := sprites[species]; ok {
		return s
	}
	return sprites["blob"]
}

var sprites = map[string]*sprite{
	"cat": {
		Idle: []string{
			`
 /\_/\
( {e} )
 = {m} =
  U U`,
			`
 /\_/\
( - - )
 = {m} =
  U U`,
			`
 /\_/\
( {e} )
 = {m}  ~
  U U`,
		},
		Sleep: []string{
			`
 /\_/\
( - - )
 = - =   z
  U U`,
			`
 /\_/\
( - - )
 = - =  zZ
  U U`,
			`
 /\_/\
( - - )
 = - = ZzZ
  U U`,
		},
		Eat: []string{
			`
 /\_/\
( {e} ) [@]
 = o =
  U U`,
			`
 /\_/\
( {e} ) [o]
 = {m} =
  U U`,
			`
 /\_/\
( {e} ) ..
 = o =
  U U`,
		},
		Play: []string{
			`
 /\_/\   *
( {e} )
 = {m} =
  U U`,
			`    *
 /\_/\
( {e} )  *
 = {m} =

`,
			`      *
 /\_/\ *
( {e} )
 = {m} =
  U U`,
		},
		Clean: []string{
			`    o
 /\_/\
( {e} ) °
 = {m} =
  U U`,
			`  O
 /\_/\ o
( {e} )
 = {m} =  O
  U U`,
			` °   o
 /\_/\
( {e} ) O
 = {m} = °
  U U`,
		},
		Dead: `
  _____
 | RIP |
 |( X X)|
 |_____|`,
	},

	"dog": {
		Idle: []string{
			`
 /^ ^\
( {e} )
 |{m}  )>
  U  U`,
			`
 /^ ^\
( - - )
 |{m}  )>
  U  U`,
			`
 /^ ^\
( {e} )
 |{m}  )> ~
  U  U`,
		},
		Sleep: []string{
			`
 /^ ^\
( - - )
 |-  )>  z
  U  U`,
			`
 /^ ^\
( - - )
 |-  )> zZ
  U  U`,
			`
 /^ ^\
( - - )
 |-  )>ZzZ
  U  U`,
		},
		Eat: []string{
			`
 /^ ^\
( {e} ) [@]
 |o  )>
  U  U`,
			`
 /^ ^\
( {e} ) [o]
 |{m}  )>
  U  U`,
			`
 /^ ^\
( {e} ) ..
 |o  )>
  U  U`,
		},
		Play: []string{
			`
 /^ ^\  *
( {e} )
 |{m}  )>
  U  U`,
			`   *
 /^ ^\
( {e} ) *
 |{m}  )>

`,
			`     *
 /^ ^\ *
( {e} )
 |{m}  )>
  U  U`,
		},
		Clean: []string{
			`   o
 /^ ^\
( {e} ) °
 |{m}  )>
  U  U`,
			`  O
 /^ ^\ o
( {e} )
 |{m}  )> O
  U  U`,
			` °  o
 /^ ^\
( {e} ) O
 |{m}  )>°
  U  U`,
		},
		Dead: `
  _____
 | RIP |
 |( X X)|
 |_____|`,
	},

	"blob": {
		Idle: []string{
			`
  .--.
 ( {e} )
 ( {m}  )
  '--'`,
			`
  .--.
 ( - - )
 ( {m}  )
  '--'`,
			`

  .--.
 ( {e} )
 ( {m}  )
  '--'`,
		},
		Sleep: []string{
			`
  .--.
 ( - - )
 ( -  )  z
  '--'`,
			`
  .--.
 ( - - )
 ( -  ) zZ
  '--'`,
			`
  .--.
 ( - - )
 ( -  )ZzZ
  '--'`,
		},
		Eat: []string{
			`
  .--.
 ( {e} )[@]
 ( o  )
  '--'`,
			`
  .--.
 ( {e} )[o]
 ( {m}  )
  '--'`,
			`
  .--.
 ( {e} )..
 ( o  )
  '--'`,
		},
		Play: []string{
			`
  .--.  *
 ( {e} )
 ( {m}  )
  '--'`,
			`   *
  .--.
 ( {e} ) *
 ( {m}  )

`,
			`     *
  .--. *
 ( {e} )
 ( {m}  )
  '--'`,
		},
		Clean: []string{
			`    o
  .--.
 ( {e} )°
 ( {m}  )
  '--'`,
			`  O
  .--. o
 ( {e} )
 ( {m}  )O
  '--'`,
			` °   o
  .--.
 ( {e} )O
 ( {m}  )°
  '--'`,
		},
		Dead: `
  _____
 | RIP |
 |( X X)|
 |_____|`,
	},
}
