// Blacklist: words that must never appear inside a generated code.
// The lists are deliberately blunt; a false positive only costs one retry.
package main

import (
	"slices"
	"strings"
)

// Uzbek words, written without apostrophes because codes cannot contain them.
var uzbekWords = []string{
	"ahmoq", "amcha", "aminga", "amingga", "bachchaboz", "bachchagar",
	"besoqolboz", "buvingni", "buzuq", "dalban", "dalbayob", "enangni",
	"eshak", "fohisha", "foxisha", "haromi", "haromzoda", "iflos",
	"itvachcha", "jalab", "jalap", "jinni", "kispurush", "kot", "kotak",
	"kotinga", "kotingga", "lanati", "manjalaqi", "maraz", "onangni",
	"onangniski", "pidaras", "qanciq", "qanjiq", "qoshmachi", "qotoq",
	"qutoq", "sassiq", "shallaqi", "sik", "sikaman", "sikay", "sikish",
	"siktir", "skaman", "skay", "skey", "suyuqoyoq", "tashoq", "tentak",
	"toshoq", "xunasa", "zinokor",
}

// Russian words in the Latin spellings people actually type.
var russianWords = []string{
	"bliad", "blya", "blyad", "blyat", "chmo", "debil", "dermo", "dolboeb",
	"dolboyob", "ebal", "eban", "ebat", "eblan", "gandon", "gnida", "govno",
	"hui", "huj", "huy", "huynya", "jopa", "kurva", "loh", "lox", "manda",
	"mraz", "mudak", "mudila", "nahui", "nahuy", "padla", "pidar", "pidor",
	"pidr", "pizda", "pizdec", "pizdets", "pohui", "pohuy", "shluha",
	"shlyuha", "srat", "suchka", "suka", "trah", "uebok", "urod", "xui",
	"xuy", "yebat", "zaebal", "zalupa", "zhopa",
}

// English words.
var englishWords = []string{
	"anal", "anus", "arse", "ass", "bastard", "bitch", "blowjob", "boner",
	"boob", "clit", "cock", "crap", "cum", "cunt", "dick", "dildo", "dyke",
	"fag", "faggot", "fuck", "fuk", "handjob", "homo", "jizz", "kike", "milf",
	"nazi", "nigga", "nigger", "penis", "piss", "porn", "prick", "pussy",
	"queer", "rape", "sex", "shit", "slut", "spic", "tits", "twat", "vagina",
	"wank", "whore", "xxx",
}

// builtinBlacklist is every built-in word, sorted and without duplicates.
var builtinBlacklist = mergeWords(uzbekWords, russianWords, englishWords)

// mergeWords joins word lists into one sorted list without duplicates.
func mergeWords(lists ...[]string) []string {
	merged := slices.Concat(lists...)
	slices.Sort(merged)
	return slices.Compact(merged)
}

// lookAlikes turns digits that read as letters into those letters, so a
// word written as "5h1t" is still found. The set is kept small on purpose:
// every extra pair also rejects more harmless codes.
var lookAlikes = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t")

// normalizeCode maps a candidate code to the form the blacklist is checked
// against: lower-case, with look-alike digits turned into letters.
func normalizeCode(code string) string {
	return lookAlikes.Replace(strings.ToLower(code))
}

// Blocked reports whether code contains a blacklisted word: a built-in one
// or one of extra (the config's blacklist). Words hide inside longer codes,
// so this is a substring search on the normalized code.
func Blocked(code string, extra []string) bool {
	normalized := normalizeCode(code)
	for _, word := range builtinBlacklist {
		if strings.Contains(normalized, word) {
			return true
		}
	}
	for _, word := range extra {
		if strings.Contains(normalized, normalizeCode(word)) {
			return true
		}
	}
	return false
}
