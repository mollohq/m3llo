package main

// Device push text by account language (specs/features/LOCALIZATION.md, 9b).
//
// Each push message has a stable key. The English table is complete. The
// other tables are empty until a translator fills them. A message that a table
// does not have falls back to the next table in the chain: the exact tag, then
// the primary subtag, then English.
//
// A message is a fmt pattern. The English patterns use %s and %d in order. A
// translation that must change the order of the values uses explicit indexes,
// for example "%[2]s ... %[1]s". The values of a key keep the order of the
// English pattern.

import (
	"context"
	"fmt"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

// pushTextEnglish is the source table. The keys are stable: a translation
// table uses the same keys.
var pushTextEnglish = map[string]string{
	// Weekly recap (recap_push.go). Values in order: name, belt, held count.
	"recap_took":          "%s took %s. You hold %d.",
	"recap_you_took":      "You took %s. You hold %d.",
	"recap_you_took_from": "You took %s from %s. You hold %d.",
	"recap_week_lead":     "Week %d recap. %s leads with %d.",

	// Belt moved (event_push.go).
	"belt_took_from_you": "%s took %s from you. You hold %d.",
	"belt_took_more":     "%s took %s and %d more. You hold %d.",
	"belt_you_took_from": "You took %s from %s. You hold %d.",
	"belt_you_took_more": "You took %s and %d more. You hold %d.",

	// Friend landed (crew_join_push.go). Values: name, (count), crew name.
	"join_one_say_hi":    "%s joined %s. Say hi.",
	"join_one":           "%s joined %s.",
	"join_one_other":     "%s and 1 other joined %s.",
	"join_others":        "%s and %d others joined %s.",
	"join_name_fallback": "A new member",

	// Mentions (push_remote.go, push_coalesce.go).
	"mentions_new":        "%d new mentions",
	"mentions_new_in":     "%d new mentions in %s",
	"mention_crews_sep":   ", ",
	"push_title_fallback": "Mello",

	// Plain messages (message_push.go, push_coalesce.go). Values: name.
	"messages_new":            "%d new messages",
	"messages_new_in":         "%d new messages in %s",
	"message_sent_gif":        "%s sent a GIF",
	"message_sent_image":      "%s sent an image",
	"message_sender_fallback": "Someone",

	// Looking to play (looking_to_play_push.go). Values: name, then the game,
	// the minutes or a second name.
	"ltp_ask":           "%s is looking to play",
	"ltp_ask_game":      "%s is looking to play %s",
	"ltp_in":            "%s is in",
	"ltp_in_due":        "%s is in, %d min",
	"ltp_two_in":        "%s and %s are in",
	"ltp_many_in":       "%d are in",
	"ltp_playing":       "%s and %s are playing",
	"ltp_nobody":        "No one this time",
	"ltp_called_off":    "%s called it off",
	"ltp_name_fallback": "A crew member",

	// Belt labels, by the title_id that the client uses (client/src/head2head.rs).
	titleTheCarry:   "The Carry",
	titleLockedIn:   "Locked In",
	titleHasALife:   "Has a Life",
	titleNightOwl:   "Night Owl",
	titleTheCollect: "The Collector",
	titleSampler:    "Sampler",
	titleOneTrick:   "One Trick",
	titleMouth:      "Mouth",
	titleMarathon:   "Marathon",
	titleLastOneUp:  "Last One Up",
	titleClipper:    "Clipper",
	titleRageQuit:   "Rage Quit",
	titleWinner:     "Winner",
}

// pushTextChinese is the zh-CN table. The words are the words of the glossary
// in TRANSLATING.md and of the client catalogue: a belt is 称号, take is 抢走,
// hold is 持有. A pattern that puts the values in another order than English
// uses explicit indexes. The push title fallback is the brand and stays.
var pushTextChinese = map[string]string{
	"recap_took":          "%s抢走了%s。你持有%d个称号。",
	"recap_you_took":      "你抢走了%s。你持有%d个称号。",
	"recap_you_took_from": "你从%[2]s手中抢走了%[1]s。你持有%[3]d个称号。",
	"recap_week_lead":     "第%d周周报。%s以%d个称号领先。",

	"belt_took_from_you": "%s从你手中抢走了%s。你持有%d个称号。",
	"belt_took_more":     "%s抢走了%s和另外%d个称号。你持有%d个称号。",
	"belt_you_took_from": "你从%[2]s手中抢走了%[1]s。你持有%[3]d个称号。",
	"belt_you_took_more": "你抢走了%s和另外%d个称号。你持有%d个称号。",

	"join_one_say_hi":    "%s加入了%s。打个招呼吧。",
	"join_one":           "%s加入了%s。",
	"join_one_other":     "%s和另外1人加入了%s。",
	"join_others":        "%s和另外%d人加入了%s。",
	"join_name_fallback": "一位新成员",

	"mentions_new":        "%d条新提及",
	"mentions_new_in":     "%[2]s中有%[1]d条新提及",
	"mention_crews_sep":   "、",
	"push_title_fallback": "Mello",

	"messages_new":            "%d条新消息",
	"messages_new_in":         "%[2]s中有%[1]d条新消息",
	"message_sent_gif":        "%s发送了一个GIF",
	"message_sent_image":      "%s发送了一张图片",
	"message_sender_fallback": "有人",

	// Looking to play. Not reviewed by a native reader yet.
	"ltp_ask":           "%s想找人一起玩",
	"ltp_ask_game":      "%s想找人一起玩%s",
	"ltp_in":            "%s参加",
	"ltp_in_due":        "%s参加，%d分钟后到",
	"ltp_two_in":        "%s和%s参加",
	"ltp_many_in":       "%d人参加",
	"ltp_playing":       "%s和%s正在玩",
	"ltp_nobody":        "这次没人参加",
	"ltp_called_off":    "%s取消了",
	"ltp_name_fallback": "一位小队成员",

	titleTheCarry:   "大腿",
	titleLockedIn:   "上头",
	titleHasALife:   "现充",
	titleNightOwl:   "夜猫子",
	titleTheCollect: "收藏家",
	titleSampler:    "尝鲜党",
	titleOneTrick:   "绝活哥",
	titleMouth:      "话痨",
	titleMarathon:   "马拉松",
	titleLastOneUp:  "守夜人",
	titleClipper:    "剪辑侠",
	titleRageQuit:   "怒退",
	titleWinner:     "赢家",
}

// pushTextSpanish is the es table. The words are the words of the glossary
// in TRANSLATING.md and of the client catalogue: a belt is título, take is
// quitar (llevarse with no source), crew is squad. "You hold %d" is written
// "Títulos: %d" so that the count needs no plural. The push title fallback is
// the brand and stays.
var pushTextSpanish = map[string]string{
	"recap_took":          "%s se llevó %s. Títulos: %d.",
	"recap_you_took":      "Te llevaste %s. Títulos: %d.",
	"recap_you_took_from": "Le quitaste %s a %s. Títulos: %d.",
	"recap_week_lead":     "Resumen de la semana %d. %s va primero con %d.",

	"belt_took_from_you": "%s te quitó %s. Títulos: %d.",
	"belt_took_more":     "%s se llevó %s y %d más. Títulos: %d.",
	"belt_you_took_from": "Le quitaste %s a %s. Títulos: %d.",
	"belt_you_took_more": "Te llevaste %s y %d más. Títulos: %d.",

	"join_one_say_hi":    "%s se unió a %s. Saluda.",
	"join_one":           "%s se unió a %s.",
	"join_one_other":     "%s y 1 más se unieron a %s.",
	"join_others":        "%s y %d más se unieron a %s.",
	"join_name_fallback": "Un miembro nuevo",

	"mentions_new":        "%d menciones nuevas",
	"mentions_new_in":     "%d menciones nuevas en %s",
	"mention_crews_sep":   ", ",
	"push_title_fallback": "Mello",

	"messages_new":            "%d mensajes nuevos",
	"messages_new_in":         "%d mensajes nuevos en %s",
	"message_sent_gif":        "%s envió un GIF",
	"message_sent_image":      "%s envió una imagen",
	"message_sender_fallback": "Alguien",

	"ltp_ask":           "%s busca partida",
	"ltp_ask_game":      "%s busca partida de %s",
	"ltp_in":            "%s se apunta",
	"ltp_in_due":        "%s se apunta, en %d min",
	"ltp_two_in":        "%s y %s se apuntan",
	"ltp_many_in":       "Se apuntan %d",
	"ltp_playing":       "%s y %s están jugando",
	"ltp_nobody":        "Nadie esta vez",
	"ltp_called_off":    "%s lo canceló",
	"ltp_name_fallback": "Alguien del squad",

	titleTheCarry:   "El Carry",
	titleLockedIn:   "Viciado",
	titleHasALife:   "Tiene Vida",
	titleNightOwl:   "Noctámbulo",
	titleTheCollect: "El Coleccionista",
	titleSampler:    "El Catador",
	titleOneTrick:   "Monotemático",
	titleMouth:      "Parlanchín",
	titleMarathon:   "Maratón",
	titleLastOneUp:  "Último en Caer",
	titleClipper:    "Clipero",
	titleRageQuit:   "Rage Quit",
	titleWinner:     "Ganador",
}

// pushTextPortuguese is the pt-BR table. The words are the words of the
// glossary and of the client catalogue: a belt is "título", take is "tomar",
// hold is "segurar", crew is "squad". The push title fallback is the brand and
// stays. Not reviewed by a native reader yet.
var pushTextPortuguese = map[string]string{
	"recap_took":          "%s tomou %s. Você segura %d.",
	"recap_you_took":      "Você tomou %s. Você segura %d.",
	"recap_you_took_from": "Você tomou %s de %s. Você segura %d.",
	"recap_week_lead":     "Resumo da semana %d. %s lidera com %d.",

	"belt_took_from_you": "%s tomou %s de você. Você segura %d.",
	"belt_took_more":     "%s tomou %s e mais %d. Você segura %d.",
	"belt_you_took_from": "Você tomou %s de %s. Você segura %d.",
	"belt_you_took_more": "Você tomou %s e mais %d. Você segura %d.",

	"join_one_say_hi":    "%s entrou em %s. Manda um oi.",
	"join_one":           "%s entrou em %s.",
	"join_one_other":     "%s e mais 1 entraram em %s.",
	"join_others":        "%s e mais %d entraram em %s.",
	"join_name_fallback": "Um membro novo",

	"mentions_new":        "%d menções novas",
	"mentions_new_in":     "%d menções novas em %s",
	"mention_crews_sep":   ", ",
	"push_title_fallback": "Mello",

	"messages_new":            "%d mensagens novas",
	"messages_new_in":         "%d mensagens novas em %s",
	"message_sent_gif":        "%s mandou um GIF",
	"message_sent_image":      "%s mandou uma imagem",
	"message_sender_fallback": "Alguém",

	"ltp_ask":           "%s quer jogar",
	"ltp_ask_game":      "%s quer jogar %s",
	"ltp_in":            "%s tá dentro",
	"ltp_in_due":        "%s tá dentro, %d min",
	"ltp_two_in":        "%s e %s tão dentro",
	"ltp_many_in":       "%d tão dentro",
	"ltp_playing":       "%s e %s tão jogando",
	"ltp_nobody":        "Ninguém desta vez",
	"ltp_called_off":    "%s cancelou",
	"ltp_name_fallback": "Alguém do squad",

	titleTheCarry:   "O Carry",
	titleLockedIn:   "Viciado",
	titleHasALife:   "Tem Vida",
	titleNightOwl:   "Coruja",
	titleTheCollect: "O Colecionador",
	titleSampler:    "Beija-Flor",
	titleOneTrick:   "Mono",
	titleMouth:      "Tagarela",
	titleMarathon:   "Maratona",
	titleLastOneUp:  "Apaga a Luz",
	titleClipper:    "Clipador",
	titleRageQuit:   "Saiu Pistola",
	titleWinner:     "Vencedor",
}

// pushTextGerman is the de table. The words are the words of the glossary:
// a belt is Titel, take is sich schnappen, hold is halten. "Titel" has the same
// form in the singular and the plural. The push title fallback is the brand
// and stays.
var pushTextGerman = map[string]string{
	"recap_took":          "%s hat sich %s geschnappt. Du hältst %d Titel.",
	"recap_you_took":      "Du hast dir %s geschnappt. Du hältst %d Titel.",
	"recap_you_took_from": "Du hast dir %s von %s geschnappt. Du hältst %d Titel.",
	"recap_week_lead":     "Rückblick Woche %d. %s führt: %d Titel.",

	"belt_took_from_you": "%s hat sich %s von dir geschnappt. Du hältst %d Titel.",
	"belt_took_more":     "%s hat sich %s und %d weitere geschnappt. Du hältst %d Titel.",
	"belt_you_took_from": "Du hast dir %s von %s geschnappt. Du hältst %d Titel.",
	"belt_you_took_more": "Du hast dir %s und %d weitere geschnappt. Du hältst %d Titel.",

	"join_one_say_hi":    "%s ist %s beigetreten. Sag Hi.",
	"join_one":           "%s ist %s beigetreten.",
	"join_one_other":     "%s und 1 weitere Person sind %s beigetreten.",
	"join_others":        "%s und %d weitere sind %s beigetreten.",
	"join_name_fallback": "Ein neues Mitglied",

	"mentions_new":        "%d neue Erwähnungen",
	"mentions_new_in":     "%d neue Erwähnungen in %s",
	"mention_crews_sep":   ", ",
	"push_title_fallback": "Mello",

	"messages_new":            "%d neue Nachrichten",
	"messages_new_in":         "%d neue Nachrichten in %s",
	"message_sent_gif":        "%s hat ein GIF geschickt",
	"message_sent_image":      "%s hat ein Bild geschickt",
	"message_sender_fallback": "Jemand",

	"ltp_ask":           "%s hat Bock zu zocken",
	"ltp_ask_game":      "%s hat Bock auf %s",
	"ltp_in":            "%s ist dabei",
	"ltp_in_due":        "%s ist dabei, in %d min",
	"ltp_two_in":        "%s und %s sind dabei",
	"ltp_many_in":       "%d sind dabei",
	"ltp_playing":       "%s und %s zocken gerade",
	"ltp_nobody":        "Diesmal niemand",
	"ltp_called_off":    "%s hat abgesagt",
	"ltp_name_fallback": "Jemand aus der Crew",

	titleTheCarry:   "Der Carry",
	titleLockedIn:   "Im Tunnel",
	titleHasALife:   "Hat ein Leben",
	titleNightOwl:   "Nachteule",
	titleTheCollect: "Der Sammler",
	titleSampler:    "Game-Hopper",
	titleOneTrick:   "One-Trick",
	titleMouth:      "Labertasche",
	titleMarathon:   "Marathon",
	titleLastOneUp:  "Letzter wach",
	titleClipper:    "Clipper",
	titleRageQuit:   "Ragequit",
	titleWinner:     "Gewinner",
}

// pushTextJapanese is the ja table. The words are the words of the ja
// glossary and of the client catalogue: a belt is 称号, take is 奪う, hold is
// 保持. A pattern that puts the values in another order than English uses
// explicit indexes. The push title fallback is the brand and stays.
var pushTextJapanese = map[string]string{
	"recap_took":          "%sが%sを奪った。今の称号は%d個。",
	"recap_you_took":      "%sを奪った。今の称号は%d個。",
	"recap_you_took_from": "%[2]sから%[1]sを奪った。今の称号は%[3]d個。",
	"recap_week_lead":     "第%d週のまとめ。%sが%d個でトップ。",

	"belt_took_from_you": "%sに%sを奪われた。今の称号は%d個。",
	"belt_took_more":     "%sが%sとほか%d個を奪った。今の称号は%d個。",
	"belt_you_took_from": "%[2]sから%[1]sを奪った。今の称号は%[3]d個。",
	"belt_you_took_more": "%sとほか%d個を奪った。今の称号は%d個。",

	"join_one_say_hi":    "%sが%sに参加。あいさつしよう。",
	"join_one":           "%sが%sに参加。",
	"join_one_other":     "%sとほか1人が%sに参加。",
	"join_others":        "%sとほか%d人が%sに参加。",
	"join_name_fallback": "新しいメンバー",

	"mentions_new":        "新しいメンション%d件",
	"mentions_new_in":     "%[2]sで新しいメンション%[1]d件",
	"mention_crews_sep":   "、",
	"push_title_fallback": "Mello",

	"messages_new":            "新着メッセージ%d件",
	"messages_new_in":         "%[2]sで新着メッセージ%[1]d件",
	"message_sent_gif":        "%sが GIF を送信",
	"message_sent_image":      "%sが画像を送信",
	"message_sender_fallback": "誰か",

	"ltp_ask":           "%sがプレイ募集中",
	"ltp_ask_game":      "%sが%sのプレイ募集中",
	"ltp_in":            "%sが参加",
	"ltp_in_due":        "%sが参加、%d分後",
	"ltp_two_in":        "%sと%sが参加",
	"ltp_many_in":       "%d人が参加",
	"ltp_playing":       "%sと%sがプレイ中",
	"ltp_nobody":        "今回は誰もいない",
	"ltp_called_off":    "%sが取りやめた",
	"ltp_name_fallback": "クルーのメンバー",

	titleTheCarry:   "キャリー",
	titleLockedIn:   "ガチ勢",
	titleHasALife:   "リア充",
	titleNightOwl:   "夜ふかし",
	titleTheCollect: "コレクター",
	titleSampler:    "つまみ食い勢",
	titleOneTrick:   "一筋",
	titleMouth:      "おしゃべり",
	titleMarathon:   "マラソン",
	titleLastOneUp:  "最後の一人",
	titleClipper:    "クリップ職人",
	titleRageQuit:   "萎え落ち",
	titleWinner:     "勝者",
}

// pushTextFrench is the fr table. The words are the words of the glossary and
// of the client catalogue: a belt is "titre", take is "piquer", hold is
// "détenir". The push title fallback is the brand and stays.
var pushTextFrench = map[string]string{
	"recap_took":          "%s a piqué %s. Tu en détiens %d.",
	"recap_you_took":      "T'as piqué %s. Tu en détiens %d.",
	"recap_you_took_from": "T'as piqué %s à %s. Tu en détiens %d.",
	"recap_week_lead":     "Récap de la semaine %d. %s mène avec %d.",

	"belt_took_from_you": "%s t'a piqué %s. Tu en détiens %d.",
	"belt_took_more":     "%s a piqué %s et %d autres. Tu en détiens %d.",
	"belt_you_took_from": "T'as piqué %s à %s. Tu en détiens %d.",
	"belt_you_took_more": "T'as piqué %s et %d autres. Tu en détiens %d.",

	"join_one_say_hi":    "%s a rejoint %s. Dis salut.",
	"join_one":           "%s a rejoint %s.",
	"join_one_other":     "%s et 1 autre ont rejoint %s.",
	"join_others":        "%s et %d autres ont rejoint %s.",
	"join_name_fallback": "Un nouveau membre",

	"mentions_new":        "%d nouvelles mentions",
	"mentions_new_in":     "%d nouvelles mentions dans %s",
	"mention_crews_sep":   ", ",
	"push_title_fallback": "Mello",

	"messages_new":            "%d nouveaux messages",
	"messages_new_in":         "%d nouveaux messages dans %s",
	"message_sent_gif":        "%s a envoyé un GIF",
	"message_sent_image":      "%s a envoyé une image",
	"message_sender_fallback": "Quelqu'un",

	"ltp_ask":           "%s veut jouer",
	"ltp_ask_game":      "%s veut jouer à %s",
	"ltp_in":            "%s est partant",
	"ltp_in_due":        "%s est partant, dans %d min",
	"ltp_two_in":        "%s et %s sont partants",
	"ltp_many_in":       "%d sont partants",
	"ltp_playing":       "%s et %s jouent",
	"ltp_nobody":        "Personne cette fois",
	"ltp_called_off":    "%s a annulé",
	"ltp_name_fallback": "Un membre du crew",

	titleTheCarry:   "Le Carry",
	titleLockedIn:   "L'Accro",
	titleHasALife:   "A une vie",
	titleNightOwl:   "Noctambule",
	titleTheCollect: "Le Collectionneur",
	titleSampler:    "Touche-à-tout",
	titleOneTrick:   "One Trick",
	titleMouth:      "La Pipelette",
	titleMarathon:   "Marathon",
	titleLastOneUp:  "Dernier Debout",
	titleClipper:    "Le Clippeur",
	titleRageQuit:   "Rage Quit",
	titleWinner:     "Gagnant",
}

// pushTextSwedish is the sv table. The words are the words of the glossary
// and of the client catalogue: a belt is "titel", take is "sno", hold is
// "hålla", the crew is "crewet". The push title fallback is the brand and stays.
var pushTextSwedish = map[string]string{
	"recap_took":          "%s snodde %s. Du håller %d.",
	"recap_you_took":      "Du snodde %s. Du håller %d.",
	"recap_you_took_from": "Du snodde %s från %s. Du håller %d.",
	"recap_week_lead":     "Summering vecka %d. %s leder med %d.",

	"belt_took_from_you": "%s snodde %s från dig. Du håller %d.",
	"belt_took_more":     "%s snodde %s och %d till. Du håller %d.",
	"belt_you_took_from": "Du snodde %s från %s. Du håller %d.",
	"belt_you_took_more": "Du snodde %s och %d till. Du håller %d.",

	"join_one_say_hi":    "%s gick med i %s. Säg hej.",
	"join_one":           "%s gick med i %s.",
	"join_one_other":     "%s och 1 till gick med i %s.",
	"join_others":        "%s och %d till gick med i %s.",
	"join_name_fallback": "En ny medlem",

	"mentions_new":        "%d nya omnämnanden",
	"mentions_new_in":     "%d nya omnämnanden i %s",
	"mention_crews_sep":   ", ",
	"push_title_fallback": "Mello",

	"messages_new":            "%d nya meddelanden",
	"messages_new_in":         "%d nya meddelanden i %s",
	"message_sent_gif":        "%s skickade en GIF",
	"message_sent_image":      "%s skickade en bild",
	"message_sender_fallback": "Någon",

	"ltp_ask":           "%s vill lira",
	"ltp_ask_game":      "%s vill lira %s",
	"ltp_in":            "%s är med",
	"ltp_in_due":        "%s är med, %d min",
	"ltp_two_in":        "%s och %s är med",
	"ltp_many_in":       "%d är med",
	"ltp_playing":       "%s och %s lirar",
	"ltp_nobody":        "Ingen den här gången",
	"ltp_called_off":    "%s blåste av det",
	"ltp_name_fallback": "En i crewet",

	titleTheCarry:   "Bäraren",
	titleLockedIn:   "I zonen",
	titleHasALife:   "Har ett liv",
	titleNightOwl:   "Nattuggla",
	titleTheCollect: "Samlaren",
	titleSampler:    "Spelhoppare",
	titleOneTrick:   "Enkelspårig",
	titleMouth:      "Pratkvarn",
	titleMarathon:   "Maraton",
	titleLastOneUp:  "Sist uppe",
	titleClipper:    "Klippare",
	titleRageQuit:   "Ragequit",
	titleWinner:     "Vinnare",
}

// pushTexts holds a table per BCP 47 tag. An empty table means "not
// translated yet": each message then shows in English.
var pushTexts = map[string]map[string]string{
	"en":    pushTextEnglish,
	"zh-CN": pushTextChinese,
	"es":    pushTextSpanish,
	"pt-BR": pushTextPortuguese,
	"de":    pushTextGerman,
	"ja":    pushTextJapanese,
	"fr":    pushTextFrench,
	"sv":    pushTextSwedish,
}

// pushTextChain is the lookup order for a tag: the exact tag, then the primary
// subtag, then English. The match ignores case, and "_" counts as "-".
func pushTextChain(lang string) []map[string]string {
	lang = strings.ReplaceAll(strings.TrimSpace(lang), "_", "-")
	var chain []map[string]string
	add := func(tag string) {
		for k, t := range pushTexts {
			if strings.EqualFold(k, tag) {
				chain = append(chain, t)
				return
			}
		}
	}
	if lang != "" {
		add(lang)
		if i := strings.Index(lang, "-"); i > 0 {
			add(lang[:i])
		}
	}
	return append(chain, pushTextEnglish)
}

// pushText is the message key in the language lang, formatted with args. The
// empty tag is English. An unknown key gives the key itself.
func pushText(lang, key string, args ...any) string {
	for _, t := range pushTextChain(lang) {
		if p, ok := t[key]; ok && p != "" {
			if len(args) == 0 {
				return p
			}
			return fmt.Sprintf(p, args...)
		}
	}
	return key
}

// pushBeltLabel is the label of a belt in lang. An unknown id gives the id.
func pushBeltLabel(lang, titleID string) string {
	return pushText(lang, titleID)
}

// pushUsers reads the users in one batched call. It returns the name shown in
// a push (the display name, else the username, cleaned for the copy rules)
// and the account language (lang_tag) of each user.
func pushUsers(ctx context.Context, nk runtime.NakamaModule, ids []string) (names, langs map[string]string) {
	names = make(map[string]string, len(ids))
	langs = make(map[string]string, len(ids))
	if len(ids) == 0 {
		return names, langs
	}
	users, err := nk.UsersGetId(ctx, ids, nil)
	if err != nil {
		return names, langs
	}
	for _, u := range users {
		name := cleanPushName(u.GetDisplayName())
		if name == "" {
			name = cleanPushName(u.GetUsername())
		}
		names[u.GetId()] = name
		langs[u.GetId()] = u.GetLangTag()
	}
	return names, langs
}
