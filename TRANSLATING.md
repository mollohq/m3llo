# Translating m3llo

This guide is for a volunteer translator. The design of the translations is in
`specs/features/LOCALIZATION.md`.

## 1. What is translated

| Part | File | State |
|---|---|---|
| The client UI | `client/translations/mello-client.pot` (the template), one `.po` per language | About 1,060 messages |
| Device push text | `backend/nakama/data/modules/push_text.go`, one table per language, keyed by name | All eight tables are complete: English and the seven languages. Each translated table is a best-try draft. It waits for a native review of the "check" rows. |
| The web site | Not in this repository | Later |

Languages: `zh-CN` (Simplified Chinese), `es` (Spanish), `pt-BR`
(Brazilian Portuguese), `de` (German), `ja` (Japanese), `fr` (French) and
`sv` (Swedish). All seven catalogues and glossary tables are complete. Each
is a best-try draft. It waits for a native review of the "check" rows
(section 5).

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

### 5.1 zh-CN (Simplified Chinese)

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
| session / sesh | One stretch of play or of voice | 开黑 | check. A session card in the feed: 开黑回放. Session replay of a stream: 直播回放. Count: 开黑次数. Use 时段 only for a span of time or a technical session ("The stream has no session to join"). Rejected: 时段 for play (a time limit), 会话 (software word). |
| match | One game round with a result | 对局 | Count: 局. |
| win, loss, draw | The result of a match | 胜, 负, 平 | Short forms W, L, D: 胜, 负, 平. "took an L": 输了一局. |
| record, win rate | Wins and losses ("4W–2L"), wins per match | 战绩, 胜率 | |
| stream (noun) | A live video of a game to the crew | 直播 | |
| stream (verb), go live, live | Start a stream, streaming now | 开播, 直播中 | |
| watch | Look at a stream | 观看 | Viewer: 观众. Host of a stream: 主播. |
| clip (noun) | The saved last 30 seconds of game or voice | 高光 | check. Measure word 条: 9条高光. Voice clip: 语音高光. Game clip: 游戏高光. "Highlight clip": 高光片段, only where the text says highlight. Rejected: 片段 (dry, a video-editing word), 剪辑 (an edit). |
| clip (verb) | Save a clip | 截取 | check. "{} clipped that": {}截了这段. |
| moment | A marked win or highlight after a match | 精彩时刻 | check. The moment kind "Highlight" is 名场面. Rejected: 高光, which is a clip. |
| recap | The weekly summary of the crew | 周报 | Rejected: 回顾 (formal). |
| feed | The list of crew events | 动态 | Crew feed: 小队动态. |
| catch up | The line of what you missed while away | 错过动态 | check. The title. The body can use 你不在时. Rejected: 离开期间 (flat), 补课 (school word). |
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

### 5.2 es (Spanish)

Neutral Spanish for Spain and Latin America. Informal address: tú.

| English | es | Note |
|---|---|---|
| m3llo, m3llo+ | m3llo, m3llo+ | Never translated. Lower case, also in an upper-case label. |
| head2head | head2head | Never translated. |
| crew | squad | check. Masculine: el squad, tu squad, los squads. The gamer word in Spanish voice chat. Rejected: clan (an esports or guild structure), equipo (a team in one match), grupo (flat, also a chat group), banda (regional). |
| member | miembro | Crewmate, crew mate: compañero de squad; in a short label "tu squad". |
| owner | líder | check. Rejected: propietario, dueño (legal words). |
| admin | admin | Plural: admins. |
| channel | canal | Voice channel: canal de voz. |
| hangout | quedada | check. A board label and "hung out in" (estuvo de quedada en). Rejected: reunión (a work meeting), pasar el rato (too long for a label). |
| voice | voz | |
| call | llamada | "in the call": en la llamada. |
| session / sesh | sesión | check. A play session: sesión de juego. Session card in the feed: resumen de la sesión. Session replay of a stream: repetición del directo. "Sign in" also uses "iniciar sesión"; the context keeps them apart. Rejected: partida (that is a match), rato (vague). |
| match | partida | Count: partidas. |
| win, loss, draw | victoria, derrota, empate | Short forms W, L, D: V, D, E ("4V–2D"). "took an L": se comió una derrota. |
| record, win rate | balance, winrate | check. "4V–2D" is the balance. Rejected: récord (a best score), tasa de victorias (formal, long). |
| stream (noun) | directo | check. El directo. Rejected: transmisión (TV word), stream (kept only in service names). |
| stream (verb), go live, live | emitir, en directo | Live badge: EN DIRECTO; in a tight badge DIRECTO. The Twitch Spanish words. |
| watch | ver | Viewer: espectador. Host of a stream: streamer. |
| clip (noun) | clip | Voice clip: clip de voz. Game clip: clip de juego. "Highlight clip": clip destacado, only where the text says highlight. Rejected: fragmento (dry), corte (an edit). |
| clip (verb) | clipear | check. "{} clipped that": {} clipeó eso. Gamer slang used on Twitch. Rejected: recortar (an edit), grabar (that is recording). |
| moment | momento | check. The moment kind "Highlight" is jugadón. Rejected: destacado (flat), clip (that is a clip). |
| recap | resumen | Weekly recap: resumen semanal. Rejected: recapitulación (formal). |
| feed | actividad | Crew feed: actividad del squad. Rejected: feed (not clear to all players), muro (Facebook word). |
| catch up | ponte al día | check. The title. The body can use "mientras no estabas". Rejected: resumen (that is the recap), novedades (shop word). |
| crew history | historial del squad | |
| belt, title | título | check. Rejected: cinturón (boxing). Belt and title are the same thing. Hold: tener. Take: quitar. |
| The Carry | El Carry | check. "carrear" is the gamer verb in Spanish. Rejected: El Salvador (a country), El Que Lleva (unclear). |
| Locked In | Viciado | check. The gamer word for a player who cannot stop. Rejected: Concentrado (too calm). |
| Has a Life | Tiene Vida | |
| Night Owl | Noctámbulo | check. Rejected: Búho Nocturno (long). |
| The Collector | El Coleccionista | |
| Sampler | El Catador | check. Tries a bit of every game. Rejected: Probador (a fitting room). |
| One Trick | Monotemático | check. Rejected: One Trick (English), Mono (also "monkey" out of context). |
| Mouth | Parlanchín | Rejected: Bocazas (rude in Spain). |
| Marathon | Maratón | |
| Last One Up | Último en Caer | check. Must differ from Night Owl. Rejected: Trasnochador (too close to Night Owl). |
| Clipper | Clipero | check. From "clipear". Rejected: Editor (a job). |
| Rage Quit | Rage Quit | check. Spanish players use the English words. Rejected: Abandono Furioso (sounds translated). |
| Winner | Ganador | |
| award | premio | |
| grinder | grindeador | check. "Grinder of the week": grindeador de la semana. Rejected: farmeador (farming is a different activity). |
| heater | en llamas | "Biggest heater": más en llamas. |
| skid | mala racha | "Roughest patch": peor racha. "{n}-loss skid": {n} derrotas seguidas. |
| streak | racha | "{n}-loss streak": racha de {n} derrotas. |
| quest | misión | Founding week: semana de fundación. |
| gift | recompensa | Sealed: sin abrir. Rejected: regalo (a present from a person). |
| banner | banner | Rejected: pancarta (a protest sign). |
| emoji | emoji | Crew emoji: emojis del squad. |
| invite | invitar, invitación | Invite link: enlace de invitación. Invite code: código de invitación. |
| join, leave | unirse, salir | |
| friend, guest | amigo, invitado | |
| mute | silenciar | Self and others; the Discord word. Unmute: activar micro. |
| deafen | ensordecer | check. The Discord word. Undeafen: dejar de ensordecer. Rejected: silenciar audio (clashes with mute). |
| push to talk | pulsar para hablar | Voice activity: actividad de voz. |
| recording | grabación | macOS "Screen Recording": Grabación de pantalla. |
| HUD, overlay | HUD, overlay | check. Rejected: superposición (long, technical). |
| chat, message, reaction | chat, mensaje, reacción | React (verb): reaccionar. |
| online, idle, invisible | en línea, ausente, invisible | Do Not Disturb: No molestar. |
| settings | ajustes | Not "configuración", which is setup. |
| setup | configuración | The first steps: configuración inicial. |
| sign in, sign out | iniciar sesión, cerrar sesión | "Log Out" is the same as sign out. |
| account | cuenta | |
| display name, nickname, handle | nick | check. Rejected: apodo (a family nickname), nombre visible (long). |
| avatar, profile | avatar, perfil | |
| game | juego | |
| key, keybind | tecla, atajo | |
| stats, verified | stats, verificado | check. Verified stats: stats oficiales. Rejected: estadísticas (too long for the labels). |
| quality | calidad | check. POTATO: PATATA. PRO-POTATO: PRO-PATATA. "calidad patata" is the meme in Spain; Latin America says "papa". |
| update | actualización | Verb: actualizar. |
| device, microphone | dispositivo, micrófono | Input: entrada. Output: salida. "micro" in a tight label. |
| image, tray, server | imagen, bandeja del sistema, servidor | |

### 5.3 pt-BR (Brazilian Portuguese)

Register: informal "você". Casual lines use spoken forms ("tá", "pra").

| English | pt-BR | Note |
|---|---|---|
| m3llo, m3llo+ | m3llo, m3llo+ | Never translated. Lower case, also in an upper-case label. |
| head2head | head2head | Never translated. |
| crew | squad | check. The gamer word in Brazil ("chama o squad"). Masculine: o squad. Rejected: time (an esports or football team), clã (a big guild), equipe (work word), galera (has no plural and no count). |
| member | membro | Crewmate, crew mate: membro do squad, or "a galera do squad" in a casual line. |
| owner | dono | check. Rejected: proprietário (legal word). |
| admin | admin | Plural: admins. |
| channel | canal | Voice channel: canal de voz. |
| hangout | resenha | check. Brazilian slang for time together with friends. Board label RESENHA; "hung out in": ficou de resenha em. Rejected: encontro (a date), bate-papo (a chat room). |
| voice | voz | The feature: chat de voz, clipe de voz, canal de voz. To be in voice, to join or leave voice: call (see the next row). |
| call | call | check. "na call", "entrar na call", "Sair da call". The Brazilian gamer word. Rejected: chamada, ligação (a phone call). |
| session / sesh | sessão | check. Game session: sessão de jogo. Session replay of a stream: replay da live. Rejected: jogatina for the UI word (play only, not voice; used only on the site as "your gaming sessions"), partida (that is a match). |
| match | partida | Count: {n} partidas. |
| win, loss, draw | vitória, derrota, empate | Short forms W, L, D: V, D, E. "took an L": perdeu uma. |
| record, win rate | campanha, winrate | check. Campanha is the football word for a W-L record. Winrate is the word that Brazilian players use. Rejected: histórico (that is "crew history"), taxa de vitórias (long, formal). |
| stream (noun) | live | Brazilian streamers and viewers say "live". Rejected: transmissão (formal, long). |
| stream (verb), go live, live | transmitir, abrir live, ao vivo | "{0} streamed {1}": {0} transmitiu {1}. Badge LIVE: AO VIVO. |
| watch | assistir | Viewer: espectador. Host of a stream: streamer. |
| clip (noun) | clipe | Voice clip: clipe de voz. Game clip: clipe de jogo. Rejected: corte (an edited cut of a long video), trecho (dry). |
| clip (verb) | clipar | check. "{} clipped that": {} clipou essa. "Clipa aí" is common gamer speech. |
| moment | momento | check. The moment kind "Highlight": lance. Rejected: destaque (dry), clipe (a clip is a different thing). |
| recap | resumo | Weekly recap: resumo da semana. Rejected: recapitulação (long, formal). |
| feed | feed | Crew feed: feed do squad. |
| catch up | o que rolou | check. The title. The body can use "enquanto você tava fora". Rejected: atualização (that is an app update), resumo (that is the recap). |
| crew history | histórico do squad | |
| belt, title | título | check. Belt and title are the same thing. Hold: segurar. Take: tomar. Rejected: cinturão (UFC sound, and the UI also says "title"). |
| The Carry | O Carry | check. Brazilian LoL players say "carry" and "carregar o time". Rejected: O Carregador (sounds like a phone charger). |
| Locked In | Viciado | check. "Tá viciado" is the gamer word for too many hours. Rejected: Concentrado, Focado (too calm). |
| Has a Life | Tem Vida | check. Rejected: Tocou Grama (meme, not known by all), Tem Vida Social (long). |
| Night Owl | Coruja | |
| The Collector | O Colecionador | |
| Sampler | Beija-Flor | check. The hummingbird tries every flower. Rejected: Experimentador (dry, long). |
| One Trick | Mono | The LoL word ("mono Yasuo"). Rejected: Monotemático. |
| Mouth | Tagarela | |
| Marathon | Maratona | Rejected: Maratonista (long). |
| Last One Up | Apaga a Luz | check. "The last one turns off the light". Must differ from Night Owl. Rejected: Último a Dormir (long). |
| Clipper | Clipador | check. Rejected: Editor (a job). |
| Rage Quit | Saiu Pistola | check. "Ficar pistola" is slang for "get angry". Rejected: Rage Quit (English), Saiu Bravo (no punch). |
| Winner | Vencedor | |
| award | prêmio | |
| grinder | farmador | check. "Farmar" is the gamer word for to grind. "Grinder of the week": Farmador da semana. Rejected: Viciado (that is Locked In). |
| heater | embalado | check. The football word for a team on a run. "Biggest heater": Mais embalado. Rejected: on fire (English). |
| skid | zica | check. Slang for a bad-luck run. "Roughest patch": Maior zica. "{n}-loss skid": zica de {n} derrotas. |
| streak | sequência | "{n}-win streak": {n} vitórias seguidas. "{n}-loss streak": {n} derrotas seguidas. |
| quest | missão | Founding week: semana de fundação. |
| gift | recompensa | Sealed: lacrado. Rejected: presente (a birthday present). |
| banner | banner | |
| emoji | emoji | Crew emoji: emoji do squad. |
| invite | convite, convidar | Invite link: link de convite. Invite code: código de convite. |
| join, leave | entrar, sair | Join a crew: entrar no squad. |
| friend, guest | amigo, convidado | |
| mute | mutar | The gamer verb. Muted: mutado. Unmute: desmutar. Also for another person or a stream. Rejected: silenciar (formal). |
| deafen | ensurdecer | check. Deafened: ensurdecido. Undeafen: reativar áudio. Rejected: desativar som (that is mute). |
| push to talk | aperte para falar | Key: tecla para falar. Voice activity: detecção de voz. |
| recording | gravação | macOS "Screen Recording": Gravação de Tela. macOS "System Settings": Ajustes do Sistema. |
| HUD, overlay | HUD, overlay | check. Both words are common in Brazilian gaming. |
| chat, message, reaction | chat, mensagem, reação | React (verb): reagir. |
| online, idle, invisible | online, ausente, invisível | Do Not Disturb: não perturbe. |
| settings | configurações | The app settings window. Rejected: ajustes (only for the macOS "System Settings"). |
| setup | configuração inicial | Not "configurações", which is "settings". |
| sign in, sign out | fazer login, sair da conta | "Log Out" is the same as sign out. Rejected: entrar (that is join). |
| account | conta | |
| display name, nickname, handle | nick | check. The gamer word. Rejected: nome de exibição (long, formal), apelido (a family nickname). |
| avatar, profile | avatar, perfil | |
| game | jogo | |
| key, keybind | tecla, atalho | Keybinds tab: ATALHOS. |
| stats, verified | stats, verificado | check. Players say "stats"; "estatísticas" is twelve letters. Verified stats: stats oficiais. |
| quality | qualidade | POTATO: BATATA. PRO-POTATO: PRO-BATATA. |
| update | atualização, atualizar | |
| device, microphone | dispositivo, microfone | Input: entrada. Output: saída. "mic" in a tight label. |
| image, tray, server | imagem, bandeja do sistema, servidor | The site never uses a word for server. |

### 5.4 de (German)

Register: informal `du`, lower case. German players use some English gamer words in voice and chat (Crew, Voice, Call, Session, Match, Clip, Stream). These words stay as loan words.

| English | de | Note |
|---|---|---|
| m3llo, m3llo+ | m3llo, m3llo+ | Never translated. Lower case, also in an upper-case label (`DIE OG-CREW, DIE m3llo MITGEPRÄGT HAT`, `m3llo+ 30 T`). |
| head2head | head2head | Never translated. The English upper-case label `HEAD2HEAD` stays as it is. |
| crew | Crew | check. Feminine: die Crew. Compounds with a hyphen: Crew-Feed, Crew-Chat, Crew-Emoji. Rejected: Clan (an organized clan with ranks), Gruppe (dry), Squad (one game mode), Team (esports). |
| member | Mitglied | Crewmate, crew mate: Crew-Mitglied; in a sentence "deine Crew" ("Schreib deiner Crew"). |
| owner | Chef | check. Rejected: Eigentümer, Besitzer (legal words), Gründer (wrong when the crew changes hands, and the step name "Crew gegründet" already uses it). |
| admin | Admin | |
| channel | Channel | check. Voice channel: Voice-Channel. Rejected: Kanal (a TV word; players say "komm in den Channel"). |
| hangout | Abhängen | check. Board label `ABHÄNGEN`; "hung out in": "abgehangen in". Rejected: Treffen (formal), Hangout (not clear in German). |
| voice | Voice | check. "im Voice" is how players say it. Voice chat: Voice-Chat. Rejected: Sprachchat (dictionary word). |
| call | Call | "in the call": im Call. Rejected: Anruf (a phone call). |
| session / sesh | Session | check. One word for play, voice and the technical session. Session replay: Session-Replay. Rejected: Sitzung (an office meeting), Zocksession (too long for labels). |
| match | Match | Plural: Matches. Rejected: Spiel (that is "game"), Partie (chess). |
| win, loss, draw | Sieg, Niederlage, Unentschieden | check. Short forms W, L, D: S, N, U, as in a German football table ("4S–2N"). "took an L": "ein L kassiert" (players use the English L in this phrase). Rejected: W/L/D (D means nothing in German). |
| record, win rate | Bilanz, Winrate | check. Rejected: Siegquote (dictionary word, players say Winrate). |
| stream (noun) | Stream | |
| stream (verb), go live, live | streamen, live gehen, live | "streaming now": streamt. |
| watch | zuschauen | Viewer: Zuschauer. Host of a stream: Streamer. |
| clip (noun) | Clip | Voice clip: Voice-Clip. Game clip: Spiel-Clip. Highlight clip: Highlight-Clip. Rejected: Ausschnitt (a video-editing word). |
| clip (verb) | clippen | check. "{} clipped that": "{} hat das geclippt". Rejected: aufnehmen (that is "record"). |
| moment | Moment | check. The moment kind "Highlight": Highlight. Rejected: Augenblick (literary). |
| recap | Rückblick | Weekly recap: Wochenrückblick. Rejected: Zusammenfassung (long, formal), Recap (not common in German UI). |
| feed | Feed | Crew feed: Crew-Feed. |
| catch up | Verpasst | check. The title and the card: Verpasst, Verpasst-Karten. The body: "Während du weg warst". Rejected: Aufholen (sounds like homework), Nachholen. |
| crew history | Crew-Chronik | Rejected: Crew-Verlauf (sounds like a browser history). |
| belt, title | Titel | check. Rejected: Gürtel (boxing). Belt and title are the same thing. Hold: halten. Take: sich schnappen ("{0} schnappte sich {1} von {2}"). "Titel" has one form in the singular and the plural. |
| The Carry | Der Carry | German players say "carry" and "gecarried". |
| Locked In | Im Tunnel | check. "im Tunnel sein" is the gamer phrase for full focus. Rejected: Fokussiert (too calm), Locked In (not understood by all). |
| Has a Life | Hat ein Leben | check. Rejected: Real Life (a noun, not a title). |
| Night Owl | Nachteule | |
| The Collector | Der Sammler | |
| Sampler | Game-Hopper | check. Rejected: Probierer (flat), Schnupperer (not a gamer word). |
| One Trick | One-Trick | check. The League of Legends word stays in English. Rejected: Spezialist (no meaning of one champion). |
| Mouth | Labertasche | |
| Marathon | Marathon | Same word in German. |
| Last One Up | Letzter wach | check. Must differ from Night Owl. Rejected: Der Letzte (an insult in German). |
| Clipper | Clipper | check. Rejected: Clip-König (gendered, long), Cutter (a job). |
| Rage Quit | Ragequit | check. German players write it as one word. Rejected: Wutausstieg (nobody says it). |
| Winner | Gewinner | |
| award | Award | Rejected: Auszeichnung (formal), Preis (also "price"). |
| grinder | Grinder | "Grinder of the week": Grinder der Woche. "grinden" is the gamer verb. |
| heater | Lauf | check. "einen Lauf haben" means a run of success. "Biggest heater": Krassester Lauf. |
| skid | Pechsträhne | "Roughest patch": Größte Pechsträhne. "{n}-loss skid": "{n} Niederlagen am Stück". |
| streak | Serie | "{n}-win streak": "{n} Siege in Folge". "{n}-loss streak": "{n} Niederlagen in Folge". |
| quest | Quest | Feminine: die Quest. Founding week: Gründungswoche. |
| gift | Belohnung | Sealed: versiegelt. Rejected: Geschenk (a present from a person). |
| banner | Banner | |
| emoji | Emoji | Crew emoji: Crew-Emoji. |
| invite | einladen, Einladung | Invite link: Einladungslink. Invite code: Einladungscode. |
| join, leave | beitreten, verlassen | "joined": "ist beigetreten". |
| friend, guest | Freund, Gast | |
| mute | Stumm | check. Label: Stumm; verb: stummschalten; "Unmute": Stumm aus. Mute another person or a stream: stummschalten. |
| deafen | Taub | check. Label: Taub; "Undeafen": Taub aus. Rejected: Ausgabe deaktivieren (long), Deafen (not understood by all). |
| push to talk | Push-to-Talk | Voice activity: Sprachaktivierung. |
| recording | Aufnahme | macOS "Screen Recording": Bildschirmaufnahme (the German macOS name). |
| HUD, overlay | HUD, Overlay | check. Both stay as in German games. |
| chat, message, reaction | Chat, Nachricht, Reaktion | React (verb): reagieren. |
| online, idle, invisible | Online, Abwesend, Unsichtbar | Do Not Disturb: Nicht stören. |
| settings | Einstellungen | macOS System Settings: Systemeinstellungen. |
| setup | Einrichtung | Not Einstellungen, which is "settings". Casual text can use "Setup" ("kein Setup"). |
| sign in, sign out | Anmelden, Abmelden | "Log Out" is the same as sign out. |
| account | Account | check. Rejected: Konto (bank word). |
| display name, nickname, handle | Anzeigename | Short form in a tight label: Name ("NAME IN DER CREW"). Rejected: Nickname (mixed with Anzeigename in other apps). |
| avatar, profile | Avatar, Profil | |
| game | Spiel | Plural: Spiele. "this week in games": "DIESE WOCHE GEZOCKT". Rejected: Game (fine in speech, odd in a UI label). |
| key, keybind | Taste, Hotkey | Keybinds (settings page): Hotkeys. Rejected: Tastenbelegung (long). |
| stats, verified | Stats, verifiziert | Verified stats: Offizielle Stats. Rejected: Statistiken (long, formal). |
| quality | Qualität | POTATO: KARTOFFEL. PRO-POTATO: PRO-KARTOFFEL. "Kartoffel-PC" is the German gamer joke. |
| update | Update | Verb: updaten / aktualisieren. |
| device, microphone | Gerät, Mikrofon | Mikro in a tight label. Input: Eingang (level), Eingabe (device). Output: Ausgang, Ausgabe. |
| image, tray, server | Bild, Infobereich, Server | check. Tray: Infobereich, the Windows name. Rejected: Tray (not in German Windows), Taskleiste (a different area). |

### 5.5 ja (Japanese)

Game titles: League of Legends is リーグ・オブ・レジェンド, TFT is チームファイト タクティクス (the official Japanese names). VALORANT, Counter-Strike 2 and The Finals keep the English title.

| English | ja | Note |
|---|---|---|
| m3llo, m3llo+ | m3llo, m3llo+ | Never translated. Lower case, also in an upper-case label. |
| head2head | head2head | Never translated. |
| crew | クルー | check. Used by streamer and gamer groups. Rejected: チーム (a sports or esports team), クラン (a competitive FPS clan), ギルド (an MMO word), 仲間 (too general for a label). |
| member | メンバー | Crewmate, crew mate: 仲間 (クルー仲間 where the word stands alone). |
| owner | オーナー | check. The Discord JP word for the maker of a server. Rejected: 所有者 (legal word), リーダー (a role, not the maker). |
| admin | 管理者 | |
| channel | チャンネル | Voice channel: ボイスチャンネル. |
| hangout | 集まり | check. Board label and "hung out in": {0}が{1}で{2}集まった. Rejected: ハングアウト (a product name), たむろ (loitering), 通話 (that is "call"). |
| voice | ボイス | Voice chat: ボイスチャット. In a stat label "VC" is also known, but the UI uses ボイス. |
| call | 通話 | "in the call": 通話中. In Japanese gamer talk 通話 is the casual word for a Discord voice call, not a phone word. |
| session / sesh | セッション | check. Count: セッション数. Session replay: セッションリプレイ. Also the technical session ("The stream has no session to join"). Rejected: 一戦 (one match only), プレイ (too general, also "play"). |
| match | 試合 | Count: {n}試合. The word of the Japanese LoL client. |
| win, loss, draw | 勝ち, 負け, 引き分け | Short forms W, L, D: 勝, 敗, 分 ("4勝2敗1分"). "took an L": 負けた. |
| record, win rate | 戦績, 勝率 | |
| stream (noun) | 配信 | |
| stream (verb), go live, live | 配信する, 配信開始, 配信中 | The LIVE badge is 配信中. |
| watch | 視聴 | Viewer: 視聴者. Host of a stream: 配信者. |
| clip (noun) | クリップ | check. The Twitch JP word. Counter 本: クリップ9本. Voice clip: ボイスクリップ. Game clip: ゲームクリップ. Rejected: 切り抜き (a fan edit of a long stream), 録画 (that is "recording"). |
| clip (verb) | クリップする | check. "{} clipped that": {}がクリップした. |
| moment | 名場面 | check. The moment kind "Highlight" is 神プレイ (gamer slang for a great play). Rejected: ハイライト (close to a clip), モーメント (not natural). |
| recap | まとめ | Weekly recap: 週間まとめ. Rejected: レポート (business word), 振り返り (long, formal). |
| feed | フィード | Crew feed: クルーのフィード. Rejected: タイムライン (long, the word of a social network). |
| catch up | 見逃し | check. The title. The body uses いない間に. Rejected: 不在中 (flat, an office word), 追いつく (a verb). |
| crew history | クルーの歴史 | |
| belt, title | 称号 | check. Rejected: ベルト (boxing only). Hold: 保持. Take: 奪う. |
| The Carry | キャリー | The Japanese gamer word for a carry. |
| Locked In | ガチ勢 | check. A player who plays seriously and a lot. Rejected: 集中 (too calm). |
| Has a Life | リア充 | |
| Night Owl | 夜ふかし | |
| The Collector | コレクター | check. |
| Sampler | つまみ食い勢 | check. Rejected: サンプラー (music gear). |
| One Trick | 一筋 | check. Gamers say "〇〇専"; that needs a name before it. Rejected: ワントリック (unknown word). |
| Mouth | おしゃべり | |
| Marathon | マラソン | |
| Last One Up | 最後の一人 | check. Must differ from Night Owl. Rejected: 徹夜王 (too close to Night Owl). |
| Clipper | クリップ職人 | check. 職人 is the gamer word for a skilled regular. Rejected: クリッパー (a nail clipper). |
| Rage Quit | 萎え落ち | check. The Japanese gamer word for a rage quit. Rejected: 台パン (desk slam, no quit). |
| Winner | 勝者 | |
| award | 賞 | |
| grinder | やり込み王 | check. "Grinder of the week": 今週のやり込み王. Rejected: 廃人 (insult). |
| heater | 絶好調 | "Biggest heater": 一番の絶好調. |
| skid | 連敗 | "Roughest patch": 今週一番の不調. "{n}-loss skid": {n}連敗. |
| streak | 連勝 | "{n}-loss streak": {n}連敗. Short "W{}" / "L{}": {}連勝 / {}連敗. |
| quest | クエスト | Founding week: 結成1週目. |
| gift | 報酬 | Sealed: 未開封. Rejected: プレゼント (a present from a person), ごほうび (childish). |
| banner | バナー | |
| emoji | 絵文字 | Crew emoji: クルー絵文字. |
| invite | 招待 | Invite link: 招待リンク. Invite code: 招待コード. |
| join, leave | 参加, 退出 | Leave a crew: クルーを抜ける (casual). |
| friend, guest | フレンド, ゲスト | |
| mute | ミュート | The same word to mute another person or a stream. |
| deafen | スピーカーミュート | check. The Discord JP word. It is long (3x the English). Rejected: 聴覚ミュート (not used). |
| push to talk | プッシュトゥトーク | The Discord JP word. Voice activity: 音声検出. |
| recording | 録画 | macOS "Screen Recording": 画面収録 (the macOS JP word). |
| HUD, overlay | HUD, オーバーレイ | check. |
| chat, message, reaction | チャット, メッセージ, リアクション | React (verb): リアクション(する). |
| online, idle, invisible | オンライン, 退席中, オフライン表示 | Do Not Disturb: 取り込み中. Discord JP words. |
| settings | 設定 | |
| setup | セットアップ | Not 設定, which is "settings". |
| sign in, sign out | ログイン, ログアウト | "Log Out" is the same as sign out. |
| account | アカウント | |
| display name, nickname, handle | 表示名 | |
| avatar, profile | アバター, プロフィール | |
| game | ゲーム | |
| key, keybind | キー, キーバインド | |
| stats, verified | スタッツ, 認証済み | check. スタッツ is the esports word. Verified stats: 公式スタッツ. Rejected: データ (too general). |
| quality | 画質 | POTATO: ポテト. PRO-POTATO: プロポテト. |
| update | アップデート | |
| device, microphone | デバイス, マイク | Input: 入力. Output: 出力. |
| image, tray, server | 画像, システムトレイ, サーバー | check. Many users say タスクトレイ; the UI keeps one word. Never サーバー on the web site. |

### 5.6 fr (French)

Register: "tu", the casual voice of French gamers on Discord and Twitch. Game titles: League of Legends, TFT, Counter-Strike 2, The Finals and Valorant have no official French name. They stay in English.

| English | fr | Note |
|---|---|---|
| m3llo, m3llo+ | m3llo, m3llo+ | Never translated. Lower case, also in an upper-case label. |
| head2head | head2head | Never translated. |
| crew | crew (le crew) | check. Masculine: "ton crew", "le fil du crew". French players use "crew" for a group of friends. Rejected: équipe (a sports or work team, flat), team (an esports roster; the site sets crews against teams), escouade (military, a game-mode word), bande (old-fashioned, also "gang"). |
| member | membre | Crewmate, crew mate: membre du crew; "Message crewmates": "Écris au crew". |
| owner | chef | check. Label: CHEF. Rejected: propriétaire (legal word), créateur (says who made it, not who leads it). |
| admin | admin | |
| channel | salon | Voice channel: salon vocal. The word French Discord users know. Rejected: canal (a TV or tech word). |
| hangout | vocal ensemble; verb: traîner en vocal | check. Board label HANGOUT: EN VOCAL. "{0} hung out in {1}": "{0} ont traîné dans {1}". Rejected: hangout (not French), soirée (one evening only). |
| voice | vocal | "Start voice": Lancer le vocal. Voice clip: clip vocal. |
| call | vocal | "in the call": en vocal. First call: Premier vocal. Rejected: appel (a phone call). |
| session / sesh | session | check. "Sesh": session. Session replay: replay de session. Game session: session de jeu. A technical session ("no session to join") also uses session. |
| match | partie | Count: partie(s). Rejected: match (sport), game (anglicism; fine in speech, unclear next to "jeu"). |
| win, loss, draw | victoire, défaite, nul | check. Short forms W, L, D become V, D, N. D means a loss in French, so the reviewer must check each record ("4V–2D"). "took an L": "s'est pris une défaite". |
| record, win rate | bilan, winrate | check. "winrate" is the word of French players. Rejected: taux de victoire (too long for the label). |
| stream (noun) | stream (le stream) | Also "live" in a badge. |
| stream (verb), go live, live | streamer, lancer un live, en live | Badge LIVE stays LIVE. "YOU ARE LIVE": T'ES EN LIVE. |
| watch | regarder | Viewer: spectateur. Host of a stream: streamer. |
| clip (noun) | clip (le clip) | check. Voice clip: clip vocal. Game clip: clip de jeu. Rejected: extrait (a press or video-editing word), séquence (dry). |
| clip (verb) | clipper | check. "{} clipped that": "{} a clippé ça". The verb of French players. Rejected: enregistrer (that is "record"). |
| moment | moment | check. The moment kind "Highlight": Grosse action. Rejected: moment fort (long), highlight (not French in a button). |
| recap | récap | Weekly recap: récap de la semaine; label RÉCAP HEBDO. Rejected: résumé (formal), bilan (that is "record"). |
| feed | fil | Crew feed: fil du crew. Rejected: flux (a tech word), feed (anglicism). |
| catch up | ce que t'as raté | check. The title and the tag. The body can use "pendant ton absence". Rejected: rattrapage (a school resit), résumé (that is "recap"). |
| crew history | historique du crew | |
| belt, title | titre | check. Belt and title are the same thing. Hold: détenir ("Tu en détiens 3"). Take: piquer ("a piqué X à Y"). Rejected: ceinture (boxing), trophée (that is "award"). |
| The Carry | Le Carry | French players say "carry" for the one who carries the team. |
| Locked In | L'Accro | check. Rejected: Concentré (too calm), Tryhard (about effort, not hours). |
| Has a Life | A une vie | check. A joke on "no-life". Rejected: Vie sociale (flat). |
| Night Owl | Noctambule | Rejected: Oiseau de nuit (long). |
| The Collector | Le Collectionneur | check. Long in a tight card. |
| Sampler | Touche-à-tout | check. Rejected: Goûteur (food word). |
| One Trick | One Trick | check. The League of Legends word in French ("OTP"). Kept in English as French players do. Rejected: Monomaniaque (clinical). |
| Mouth | La Pipelette | check. A chatterbox. Rejected: La Grande Gueule (rude). |
| Marathon | Marathon | Same word in French. Rejected: Endurant (flat). |
| Last One Up | Dernier Debout | check. Must differ from Night Owl. Rejected: Couche-tard (same as Night Owl). |
| Clipper | Le Clippeur | check. Rejected: Monteur (a video-editing job). |
| Rage Quit | Rage Quit | check. French players say "rage quit". Rejected: Abandon rageur (stiff). |
| Winner | Gagnant | Rejected: Vainqueur (formal). |
| award | trophée | The weekly fun prize in the recap. |
| grinder | grinder | check. "Grinder of the week": Grinder de la semaine. Rejected: no-life (clashes with "A une vie"), bosseur (work word). |
| heater | en feu | "Biggest heater": Le plus en feu. |
| skid | série noire | "{n}-loss skid": série noire de {n}. "Roughest patch": La pire passe. |
| streak | série | "{n}-win streak": {n} victoires d'affilée. "{n}-loss streak": {n} défaites d'affilée. |
| quest | quête | Founding week: semaine de fondation. |
| gift | récompense | check. Sealed: scellée. Rejected: cadeau (a present from a person). |
| banner | bannière | |
| emoji | emoji | Crew emoji: emojis du crew. |
| invite | inviter, invitation | Invite link: lien d'invitation. Invite code: code d'invitation. |
| join, leave | rejoindre, quitter | |
| friend, guest | ami, invité | |
| mute | couper le micro | Label: Micro coupé. Mute another person or a stream: rendre muet, couper le son. |
| deafen | mettre en sourdine | check. The Discord word in French. Button: Sourdine. State: En sourdine. Undeafen: Réactiver le son. Rejected: couper le son (that is "mute a stream"). |
| push to talk | appuyer pour parler | Voice activity: activité vocale. |
| recording | enregistrement | macOS "Screen Recording": Enregistrement de l'écran. |
| HUD, overlay | HUD, overlay | check. French players say "overlay". Rejected: superposition (a long, literal word). |
| chat, message, reaction | chat, message, réaction | React (verb): réagir. |
| online, idle, invisible | en ligne, inactif, invisible | Do Not Disturb: Ne pas déranger. |
| settings | paramètres | |
| setup | configuration | Not "paramètres", which is "settings". |
| sign in, sign out | se connecter, se déconnecter | "Log Out" is the same as sign out. Network "Connecting…" uses "Connexion…". |
| account | compte | |
| display name, nickname, handle | pseudo | |
| avatar, profile | avatar, profil | |
| game | jeu | |
| key, keybind | touche, raccourci | |
| stats, verified | stats, vérifié | Verified stats: stats officielles. |
| quality | qualité | POTATO: PATATE. PRO-POTATO: PATATE PRO. |
| update | mise à jour | Short form MAJ only where the space is tight. |
| device, microphone | périphérique, micro | Input: entrée. Output: sortie. |
| image, tray, server | image, zone de notification, serveur | The site never uses a word for "server". |

### 5.7 sv (Swedish)

| English | sv | Note |
|---|---|---|
| m3llo, m3llo+ | m3llo, m3llo+ | Never translated. Lower case, also in an upper-case label. |
| head2head | head2head | Never translated. |
| crew | crew (crewet, crewen) | Neuter ("ett crew"). The English word is kept as the product term. Compounds as one word: crewflöde, crewhistorik, crewemoji, crewbanner. Rejected: gäng (close to "gang"), lag (a sports or esports team), klan (a guild word). |
| member | medlem | Crewmate, crew mate: polare. "Message crewmates": Skriv till crewet. |
| owner | ägare | check. The word in Swedish chat apps. Rejected: grundare (a founder, not a role), boss (too much slang for a role label). |
| admin | admin | |
| channel | kanal | Voice channel: röstkanal. |
| hangout | häng | check. A board label (HÄNG) and "hung out in": hängde i. Rejected: umgänge (formal), träff (a planned meeting). |
| voice | röst | Voice chat: röstchatt. "In voice": i röst. |
| call | röst | "in the call": i röst. "First call": Första röstchatten. Rejected: samtal (a phone call). |
| session / sesh | pass | check. A stretch of play: spelpass. Voice session: röstpass. A session card in the feed: passrepris. Session replay of a stream: streamrepris. Count: antal pass. Use "session" only for a technical session ("The stream has no session to join"). Rejected: session for play (a software word), omgång (one round). |
| match | match | Plural: matcher. |
| win, loss, draw | vinst, förlust, oavgjort | Short forms W, L, D: V, F, O (the letters of Swedish sports tables). "took an L": åkte på en förlust. |
| record, win rate | facit, vinstprocent | "4W–2L": 4V–2F. |
| stream (noun) | stream (en stream, streamen) | The loan word that Swedish gamers use. Rejected: livesändning (TV word). |
| stream (verb), go live, live | streama, gå live, live | |
| watch | kolla | "Watch {}": Kolla på {}. Viewer: tittare. Host of a stream: streamer. Rejected: titta (correct but flat; used only in "tittare"). |
| clip (noun) | klipp (ett klipp, klippet, klipp, klippen) | Voice clip: röstklipp. Game clip: spelklipp. |
| clip (verb) | klippa | "{} clipped that": {} klippte det. |
| moment | ögonblick | check. The moment kind "Highlight": Höjdpunkt. |
| recap | summering | Weekly recap: veckosummering, in a label VECKANS SUMMERING. Rejected: sammanfattning (long, school word). |
| feed | flöde | Crew feed: crewflöde. |
| catch up | ikapp | check. The title: Ikapp. The body: Medan du var borta. Rejected: återblick (formal). |
| crew history | crewhistorik | |
| belt, title | titel (titeln, titlar) | check. Belt and title are the same thing. Hold: hålla ("håller titeln", a sports phrase). Take: sno (casual "snatch"). Rejected: bälte (boxing only). |
| The Carry | Bäraren | check. Swedish gamers say "carry" as a verb ("han carryade"); Bäraren keeps the meaning in Swedish. Rejected: Carryn (an English noun with a Swedish ending). |
| Locked In | I zonen | check. Rejected: Fokuserad (too calm). |
| Has a Life | Har ett liv | |
| Night Owl | Nattuggla | |
| The Collector | Samlaren | |
| Sampler | Spelhoppare | check. A player who hops between many games. Rejected: Provare (flat). |
| One Trick | Enkelspårig | check. A Swedish word for "one-track mind". Rejected: One-trick (English, only LoL players use it). |
| Mouth | Pratkvarn | |
| Marathon | Maraton | |
| Last One Up | Sist uppe | check. Must differ from Night Owl. Rejected: Sömnlös (a health word). |
| Clipper | Klippare | check. Rejected: Klipparen (with the article it is a hair clipper first). |
| Rage Quit | Ragequit | check. The word Swedish gamers use ("ragequitta"). Rejected: Ilskeavhopp (nobody says it). |
| Winner | Vinnare | |
| award | pris (priset, priser) | |
| grinder | slitvarg | check. "Grinder of the week": Veckans slitvarg. Rejected: grindare (an English loan with few users). |
| heater | het svit | "Biggest heater": Hetaste sviten. |
| skid | svacka | "Roughest patch": Tyngsta svackan. "{n}-loss skid": {n} förluster i rad. |
| streak | svit | "{n}-win streak": {n} vinster i rad. "{n}-loss streak": {n} förluster i rad. |
| quest | uppdrag | Founding week: grundarveckan. FIRST WEEK: FÖRSTA VECKAN. |
| gift | belöning | Sealed: oöppnad. Rejected: present, gåva (a birthday gift). |
| banner | banner | Crew banner: crewbanner. |
| emoji | emoji | Crew emoji: crewemoji. |
| invite | bjuda in (verb), inbjudan (noun) | Invite link: inbjudningslänk. Invite code: inbjudningskod. |
| join, leave | gå med, lämna | Join voice: gå med i röst. |
| friend, guest | kompis, gäst | Rejected: vän (formal in a game app). |
| mute | muta | Mute your microphone: muta, Unmute: avmuta, Muted: mutad. Mute another person or a stream: tysta. |
| deafen | stäng av ljud | check. Undeafen: slå på ljud. Deafened: ljud av. Rejected: döva (sounds like a medical word). |
| push to talk | tryck för att prata | Voice activity: röstaktivering. |
| recording | inspelning | macOS "Screen Recording": Skärminspelning (the macOS word). |
| HUD, overlay | HUD, overlay | check. Rejected: överlägg (nobody says it). |
| chat, message, reaction | chatt, meddelande, reaktion | React (verb): reagera. |
| online, idle, invisible | online, inaktiv, osynlig | Do Not Disturb: Stör ej. |
| settings | inställningar | |
| setup | konfig | check. Casual. Not "inställningar", which is "settings". Rejected: konfiguration (long, IT word). |
| sign in, sign out | logga in, logga ut | "Log Out" is the same as sign out. |
| account | konto | |
| display name, nickname, handle | visningsnamn | "namn" in a tight label. |
| avatar, profile | avatar, profil | |
| game | spel | |
| key, keybind | tangent, kortkommando | |
| stats, verified | statistik, verifierad | Verified stats: officiell statistik. |
| quality | kvalitet | POTATO: POTATIS. PRO-POTATO: PROFFSPOTATIS. |
| update | uppdatering (noun), uppdatera (verb) | |
| device, microphone | enhet, mikrofon | Input: ingång. Output: utgång. "mick" in a tight label. |
| image, tray, server | bild, systemfältet, server | "server" in the client only. The site never uses a word for "server". |

## 6. Style

- Keep it short. A label is one or two words.
- Use the gaming register of the product. Write as players talk to friends.
- No formal register where the English is casual. Chinese: 你, not 您.
- One word, one meaning. Do not add text that the English does not have.
