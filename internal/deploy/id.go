package deploy

import (
	"fmt"
	"math/rand/v2"
)

var adjectives = []string{
	"bold", "calm", "cool", "dark", "deep", "fair", "fast", "firm", "glad", "gold",
	"gray", "keen", "kind", "lean", "live", "long", "loud", "mild", "neat", "pale",
	"pink", "pure", "rare", "real", "rich", "safe", "slim", "soft", "tall", "tame",
	"thin", "true", "vast", "warm", "wide", "wild", "wise", "aged", "blue", "bright",
	"clean", "crisp", "dry", "flat", "free", "fresh", "green", "prime", "quick", "sharp",
}

var nouns = []string{
	"ash", "bay", "bee", "bow", "cap", "dew", "elk", "elm", "fin", "fox",
	"gem", "hay", "hen", "ivy", "jay", "jet", "key", "koi", "lark", "leaf",
	"lime", "lynx", "moth", "oak", "oat", "ore", "owl", "paw", "peak", "pine",
	"plum", "pond", "reed", "reef", "rose", "sage", "seal", "star", "swan", "tide",
	"twig", "vale", "vine", "wasp", "wave", "wren", "yak", "yew", "fern", "hawk",
}

var verbs = []string{
	"aim", "arc", "ask", "bid", "bow", "cry", "dip", "dig", "dot", "dye",
	"eat", "end", "fit", "fly", "glow", "grow", "hum", "jog", "jot", "jump",
	"kick", "knit", "land", "leap", "lift", "link", "loop", "mark", "melt", "mix",
	"nod", "open", "pace", "pass", "pick", "plan", "pull", "push", "rest", "ride",
	"ring", "rise", "roll", "rush", "sail", "seek", "sing", "skip", "spin", "swim",
}

// GenerateID returns a random deploy ID in "adjective-noun-verb" format.
func GenerateID() string {
	a := adjectives[rand.IntN(len(adjectives))]
	n := nouns[rand.IntN(len(nouns))]
	v := verbs[rand.IntN(len(verbs))]
	return fmt.Sprintf("%s-%s-%s", a, n, v)
}

// GenerateUniqueID generates a deploy ID that doesn't conflict with any existing IDs.
func GenerateUniqueID(existing []string) string {
	set := make(map[string]bool, len(existing))
	for _, id := range existing {
		set[id] = true
	}
	for range 100 {
		id := GenerateID()
		if !set[id] {
			return id
		}
	}
	// Extremely unlikely fallback — append random suffix.
	return fmt.Sprintf("%s-%d", GenerateID(), rand.IntN(9999))
}
