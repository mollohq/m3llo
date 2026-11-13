# Translating m3llo

This guide is for a volunteer translator. The design of the translations is in
`specs/features/LOCALIZATION.md`.

## 1. What is translated

| Part | File | State |
|---|---|---|
| The client UI | `client/translations/mello-client.pot` (the template), one `.po` per language | About 1,060 messages |
| Device push text | `backend/nakama/data/modules/push_text.go`, one table per language, keyed by name | The English and the `zh-CN` tables are complete. The other tables are empty. |
| The web site | Not in this repository | Later |

Languages: `zh-CN` (Simplified Chinese, waits for a native review of the
"check" rows). Planned: `es`, `pt-BR`,
`de`, `ja`, `fr`, `sv`.

## 2. Translate the client

1. Copy `client/translations/mello-client.pot` to
   `client/translations/<lang>/LC_MESSAGES/mello-client.po`. The folder name
   is the language tag: `zh-CN`, `pt-BR`, `de`.
2. In the header, set `Language: <lang>` and the `Plural-Forms:` line of the
   language. Chinese: `nplurals=1; plural=0;`.
3. Translate each `msgstr`. Use Poedit or a text editor. An empty `msgstr`
   shows the English text.

- Keep each placeholder: `{}`, `{0}`, `{1}`, `{n}`. You can move `{0}` and
  `{1}` in the sentence. Do not change their numbers.
- Keep the `msgctxt` lines. A context (`verb`, `noun`, `unit`, `format`)
  tells which meaning of the English word the message has.
- Read the `#.` comment above a message. It tells what the values are.
- A plural has `msgstr[0]`, `msgstr[1]` and more. Fill as many forms as the
  language has. Chinese has one form.
- An upper-case label stays upper case in a script with case.
- Keep `\n` line breaks. Slint shows a `#, fuzzy` translation too, so leave
  the `msgstr` empty when you are not sure.

Keep as they are: `m3llo`, `m3llo+`, `head2head`, a game title with no official local name (League of Legends is 英雄联盟, TFT is 云顶之弈),
`GameName#TAG`, `@` handles, URLs, keyboard key names (`F9`, `Enter`), units
(`fps`, `MB`, `ms`) and the names of other services (Riot, Steam, GIPHY).

Push text: add each key of the English table to the table of your language.
Keep each `%s` and `%d`. Use `%[2]s ... %[1]s` to change the order.

## 3. Check your work

| Check | Command | What it does |
|---|---|---|
| Build | `cargo build -p mello-client` | Bundles each catalog. A `.po` file that does not parse, or a bad `Plural-Forms` line, stops the build. |
| Run in your language | `MELLO_LANG=<lang> cargo run -p mello-client` | Starts the app in that language (`client/src/locale.rs`). |
| Layout | `./scripts/l10n-pseudo-shots.sh <out-dir>` | Renders each panel preview in English and in the pseudo language `qps`. It shows text that is too long for its box. It needs `slint-viewer` 1.18.1. |
| Push tables | `go test ./...` in `backend/nakama/data/modules` | Checks that a filled table has all the keys and fits the limits (title 64 characters, body 178). |

Check each placeholder yourself. In the app, look at the screens with long
text.

## 4. Send your translation

Open a pull request with the one `.po` file. Add the push table in
`push_text.go` if you filled it.

The reviewer looks at the header (`Language`, `Plural-Forms`), each
placeholder and `msgctxt`, the glossary words, the register, and text that
does not fit in the app or in the layout render.

## 5. Glossary

One English word has one translation in the whole UI, also when the dictionary
has a different word. "check": a native reviewer must look at the row first.

| English | Meaning | zh-CN | Note |
|---|---|---|---|
| m3llo, m3llo+ | The brand and the paid plan | m3llo, m3llo+ | Never translated. Lower case, also in an upper-case label. |
| head2head | The board of titles in the crew | head2head | Never translated. |
| crew | The group of friends that plays together | 小队 | check. Rejected: 战队 (an esports team), 车队 (one evening only). |
| member | A person in a crew | 成员 | Crewmate, crew mate: 队友. |
| owner | The member who made the crew | 队长 | check. Rejected: 所有者 (legal word). |
| admin | A member with the rights to manage the crew | 管理员 | |
| channel | A voice channel of the crew | 频道 | Voice channel: 语音频道. |
| hangout | Time together in a voice channel | 开黑 | check. A board label and "hung out in". Rejected: 语音聊天室. |
| voice | Voice chat | 语音 | |
| call | People in a voice channel now | 语音 | "in the call": 在语音中. Rejected: 通话 (a phone call). |
| session / sesh | One stretch of play or of voice | 时段 | check. Game session: 游戏时段. Rejected: 会话 (software word). |
| match | One game round with a result | 对局 | Count: 局. |
| win, loss, draw | The result of a match | 胜, 负, 平 | Short forms W, L, D: 胜, 负, 平. "took an L": 输了一局. |
| record, win rate | Wins and losses ("4W–2L"), wins per match | 战绩, 胜率 | |
| stream (noun) | A live video of a game to the crew | 直播 | |
| stream (verb), go live, live | Start a stream, streaming now | 开播, 直播中 | |
| watch | Look at a stream | 观看 | Viewer: 观众. Host of a stream: 主播. |
| clip (noun) | The saved last 30 seconds of game or voice | 片段 | check. Voice clip: 语音片段. Game clip: 游戏片段. Rejected: 剪辑 (an edit). |
| clip (verb) | Save a clip | 截取 | check. "{} clipped that": {}截了这段. |
| moment | A marked win or highlight after a match | 精彩时刻 | check. Rejected: 高光, kept for the moment kind "Highlight". |
| recap | The weekly summary of the crew | 周报 | Rejected: 回顾 (formal). |
| feed | The list of crew events | 动态 | Crew feed: 小队动态. |
| catch up | The line of what you missed while away | 离开期间 | check. Rejected: 补课 (school word). |
| crew history | All past weeks of the crew | 小队历史 | |
| belt, title | A title that one member holds and another can take | 称号 | check. Rejected: 金腰带 (boxing). Belt and title are the same thing. Hold: 持有. Take: 抢走. |
| The Carry | Most hours in the top shared game | 大腿 | "抱大腿" is the gamer word for a carry. |
| Locked In | Most game hours this week | 上头 | check. Rejected: 专注 (too calm). |
| Has a Life | Fewest game hours | 现充 | |
| Night Owl | Latest local time of a last session | 夜猫子 | |
| The Collector | Most installed games | 收藏家 | check. Rejected: 喜加一 (free games only). |
| Sampler | Most different games | 尝鲜党 | check. Rejected: 尝鲜者. |
| One Trick | Most play time in one game | 绝活哥 | The League of Legends word for a one-trick. |
| Mouth | Most voice minutes | 话痨 | |
| Marathon | Longest single session | 马拉松 | Rejected: 铁人. |
| Last One Up | Latest session end | 守夜人 | check. Must differ from Night Owl. Rejected: 熬夜王. |
| Clipper | Most clips | 剪辑侠 | check. Rejected: 剪辑师 (a job). |
| Rage Quit | Most sessions of 2 to 10 minutes | 怒退 | check. Rejected: 秒退 (no anger). |
| Winner | Most win moments | 赢家 | |
| award | A weekly fun prize in the recap | 奖项 | |
| grinder | The member with the most matches | 肝帝 | "Grinder of the week": 本周肝帝. |
| heater | A run of wins | 手感火热 | "Biggest heater": 手感最热. |
| skid | A run of losses | 连跪 | "Roughest patch": 本周最背. "{n}-loss skid": 连跪{n}局. |
| streak | A run of wins in a row | 连胜 | "{n}-loss streak": 连败{n}局. |
| quest | A step of the first-week crew tasks | 任务 | Founding week: 建队首周. |
| gift | What a finished quest unlocks | 奖励 | Sealed: 未开启. Rejected: 礼物. |
| banner | The wide crew image | 横幅 | |
| emoji | A small image in chat, also crew emoji | 表情 | Crew emoji: 小队表情. |
| invite | Ask a person into the crew | 邀请 | Invite link: 邀请链接. Invite code: 邀请码. |
| join, leave | Go into or out of a crew or voice | 加入, 退出 | |
| friend, guest | A person you know, a non-member in voice | 好友, 访客 | |
| mute | Turn off your microphone | 闭麦 | Mute another person or a stream: 静音. |
| deafen | Turn off all sound you hear | 拒听 | check. The Discord word. Rejected: 关闭声音. |
| push to talk | Talk only while a key is down | 按键说话 | Voice activity: 语音激活. |
| recording | The capture of the screen or game | 录制 | macOS "Screen Recording": 屏幕录制. |
| HUD, overlay | The in-game layer of m3llo | HUD, 游戏内覆盖层 | check. |
| chat, message, reaction | Text in the crew, an emoji on a message | 聊天, 消息, 表情回应 | React (verb): 回应. |
| online, idle, invisible | Presence states | 在线, 闲置, 隐身 | Do Not Disturb: 请勿打扰. |
| settings | The settings window | 设置 | |
| setup | The first steps after install | 配置 | Not 设置, which is "settings". |
| sign in, sign out | Start and end a session on an account | 登录, 退出登录 | "Log Out" is the same as sign out. |
| account | The m3llo account or a linked account | 账号 | Rejected: 账户 (bank word). |
| display name, nickname, handle | The name others see | 昵称 | |
| avatar, profile | Your picture, your name and picture | 头像, 个人资料 | |
| game | A video game | 游戏 | |
| key, keybind | A keyboard key for an action | 按键, 快捷键 | |
| stats, verified | Numbers about play, checked with the game server | 数据, 已验证 | Verified stats: 官方数据. |
| quality | Video quality of a stream or clip | 画质 | POTATO: 土豆. PRO-POTATO: 高级土豆. |
| update | A new version of the app | 更新 | |
| device, microphone | An audio device, the input device | 设备, 麦克风 | Input: 输入. Output: 输出. 麦 in a tight label. |
| image, tray, server | A picture, the OS system tray, the m3llo server | 图片, 系统托盘, 服务器 | |

## 6. Style

- Keep it short. A label is one or two words.
- Use the gaming register of the product. Write as players talk to friends.
- No formal register where the English is casual. Chinese: 你, not 您.
- One word, one meaning. Do not add text that the English does not have.
