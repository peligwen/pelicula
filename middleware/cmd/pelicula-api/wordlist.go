package main

// passphraseWords is a curated mix of English and Spanish words — adjectives,
// nouns, verbs, adverbs, and prepositions — ranging from 3 to 7 letters.
// Used by generateReadablePassword via weightedPassphraseWords.
var passphraseWords = []string{
	// ── 3-letter words (English) ─────────────────────────────────────────────
	"arc", "bay", "den", "dew", "dim", "dry", "dye", "elm",
	"far", "few", "fog", "gem", "glow", "ice", "ivy", "jet",
	"joy", "key", "low", "lux", "mar", "mew", "new", "oak",
	"odd", "orb", "raw", "ray", "red", "roe", "sea", "shy",
	"sky", "sly", "tar", "taw", "wax", "web", "yew",

	// ── 3-letter words (Spanish) ─────────────────────────────────────────────
	"ago", "ala", "ama", "arco", "cal", "cielo", "doy", "era",
	"luz", "mar", "mas", "ola", "paz", "red", "rio", "sol",
	"sur", "via", "voz",

	// ── 4-letter words (English) ─────────────────────────────────────────────
	"arch", "beam", "bold", "bolt", "calm", "cave", "clan",
	"clay", "cold", "cool", "dark", "dawn", "dune", "dusk",
	"dust", "echo", "epic", "even", "fair", "fern", "fine",
	"firm", "flat", "flow", "foam", "fold", "free", "full",
	"gale", "glad", "glow", "gold", "grim", "haze", "high",
	"hill", "husk", "jade", "just", "keen", "kind", "kite",
	"lake", "lark", "leaf", "lean", "loch", "lone", "loom",
	"lore", "loud", "lush", "mild", "mist", "moon", "moss",
	"mute", "myth", "nest", "next", "nigh", "noon", "nord",
	"null", "open", "oval", "pale", "path", "pine", "pool",
	"pure", "rain", "rare", "rune", "rust", "safe", "sage",
	"salt", "sand", "silk", "slim", "slow", "snow", "soft",
	"soul", "sour", "star", "stem", "stir", "tale", "tall",
	"tame", "tide", "tilt", "time", "tiny", "tusk", "veil",
	"vine", "void", "wake", "warm", "wave", "wide", "wild",
	"wind", "wire", "wise", "wood", "wiry", "yore",

	// ── 4-letter words (Spanish) ─────────────────────────────────────────────
	"agil", "alba", "alto", "apto", "arco", "azul", "buen",
	"caer", "cima", "claro", "copa", "duna", "faro", "faro",
	"flor", "frio", "gato", "gris", "hoja", "humo", "isla",
	"lago", "lava", "leve", "loco", "loma", "luna", "malo",
	"mero", "mudo", "neto", "nube", "olas", "palo", "pico",
	"pino", "rayo", "roca", "rojo", "ruta", "sano", "solo",
	"tiza", "unico", "vago", "vela", "vivo", "yerto",

	// ── 5-letter words (English) ─────────────────────────────────────────────
	"above", "after", "agate", "along", "amber", "amble", "among",
	"ample", "argon", "arise", "arrow", "atlas", "below", "birch",
	"black", "blade", "blaze", "bloom", "blown", "blunt", "blush",
	"brave", "briar", "brine", "brisk", "brook", "brush", "cedar",
	"charm", "chase", "chasm", "clean", "clear", "cliff", "climb",
	"cloak", "close", "cloud", "coast", "comet", "coral", "crest",
	"crisp", "crown", "curve", "delta", "dense", "depth", "drift",
	"droit", "dusky", "eager", "early", "earthy", "ember", "empty",
	"equal", "exact", "faint", "falls", "feast", "fiery", "field",
	"fjord", "flare", "flame", "flash", "fleet", "flint", "float",
	"flood", "floor", "flows", "flute", "forge", "frost", "giant",
	"glade", "glide", "glint", "globe", "grail", "grand", "grave",
	"great", "green", "grove", "happy", "harsh", "haven", "heavy",
	"helix", "ivory", "jewel", "joust", "knoll", "lance", "large",
	"laser", "leaps", "ledge", "lifts", "light", "limit", "lofty",
	"loops", "lotus", "lower", "lucky", "lunar", "lusty", "maple",
	"marsh", "moody", "mount", "moves", "muddy", "night", "noble",
	"north", "noted", "ocean", "opens", "orbit", "outer", "ovoid",
	"oxbow", "ozone", "pearl", "petal", "pillar", "plain", "plant",
	"plume", "polar", "prime", "proud", "prism", "quartz", "quiet",
	"rapid", "reach", "ready", "realm", "reeds", "ridge", "rifle",
	"rings", "risen", "rises", "river", "roams", "roars", "rocky",
	"rough", "royal", "rural", "rusty", "shade", "shaft", "shard",
	"sharp", "sheer", "shell", "shine", "shiny", "shore", "short",
	"shout", "sight", "sigma", "silver", "skies", "slash", "sleek",
	"slope", "smoke", "snowy", "sobre", "solid", "souls", "spare",
	"spark", "spine", "spire", "spore", "spray", "stand", "stark",
	"stems", "stern", "still", "stone", "storm", "stout", "sweep",
	"sweet", "swift", "swims", "sways", "thorn", "thunder", "tiger",
	"tinge", "tonal", "torso", "tower", "trace", "trail", "treks",
	"ultra", "under", "upper", "vapor", "vault", "verde", "veers",
	"vista", "vivid", "viejo", "walks", "water", "white", "winds",
	"wings", "woken", "world", "young",

	// ── 5-letter words (Spanish) ─────────────────────────────────────────────
	"agudo", "ajena", "amado", "ambar", "arduo", "arbol", "arena",
	"astro", "barro", "bella", "bello", "brava", "breve", "brisa",
	"bruma", "buena", "campo", "carta", "casto", "cielo", "claro",
	"cobre", "comun", "coral", "corto", "culto", "cumbre", "curso",
	"danza", "denso", "delta", "digno", "dulce", "duros", "entra",
	"espejo", "feliz", "fiero", "firme", "flaco", "flota", "fluye",
	"folio", "fuego", "gatos", "golfo", "guapo", "habil", "hielo",
	"hondo", "igual", "inerte", "joven", "justo", "largo", "lento",
	"libre", "limpio", "lindo", "llano", "llama", "llave", "lluvia",
	"local", "logico", "lozano", "lujoso", "lunar", "maduro", "magro",
	"manso", "mares", "mayor", "menor", "mismo", "monte", "mueve",
	"niebla", "nieves", "noche", "norte", "nubes", "nuevo", "opaco",
	"pardo", "pobre", "pleno", "playa", "pluma", "polvo", "polar",
	"prado", "pronto", "propio", "raiz", "recto", "ronda", "rubio",
	"rugoso", "sabio", "sagaz", "salta", "sanos", "selva", "senda",
	"sereno", "sigue", "sigma", "simple", "sobre", "soles", "suave",
	"suelo", "suena", "surca", "tierno", "tierra", "tigre", "torpe",
	"tosco", "tronco", "unico", "vasto", "venas", "verde", "viento",
	"viejo", "vuela",

	// ── 6-letter words (English) ─────────────────────────────────────────────
	"ablaze", "aerial", "alight", "alpine", "antler", "arctic", "astral",
	"autumn", "blazed", "bolder", "bridge", "canopy", "cinder", "coarse",
	"cobalt", "cradle", "crimson", "curved", "fading", "finite", "floats",
	"frozen", "gazing", "gentle", "gilded", "glacial", "golden", "gravel",
	"gritty", "hallowed", "harbor", "hollow", "inward", "island", "jagged",
	"jewels", "joyful", "kindle", "lavish", "liquid", "lively", "lonely",
	"luster", "mantle", "mortal", "mossy", "mellow", "nimble", "onward",
	"primal", "refuge", "ripple", "serene", "silver", "silent", "smokey",
	"spiral", "stable", "starry", "summer", "tender", "timber", "torrent",
	"tunnel", "veiled", "velvet", "wander", "winter", "wooden", "yellow",

	// ── 6-letter words (Spanish) ─────────────────────────────────────────────
	"agitas", "alegre", "altivo", "bailan", "calido", "celeste", "cielos",
	"claris", "colina", "curvas", "dorado", "escala", "fuerte", "fresco",
	"helada", "ligero", "limpio", "lluvia", "marino", "madera", "nativo",
	"oscuro", "paloma", "piedra", "plenas", "pueblo", "rapido", "rizado",
	"rocoso", "saltar", "seguro", "sereno", "sierras", "solido", "sonido",
	"suaves", "tejado", "velero", "viento", "viñedo", "volver",

	// ── 7-letter words (English) ─────────────────────────────────────────────
	"blazing", "cascade", "coastal", "crystal", "drifted", "eternal",
	"feather", "glisten", "horizon", "lantern", "morning", "orbital",
	"phantom", "shimmer", "silence", "skyline", "spirals", "sunrise",
	"thermal", "verdant", "weather",

	// ── 7-letter words (Spanish) ─────────────────────────────────────────────
	"callado", "celeste", "cumulus", "dorados", "espiral", "estrella",
	"latente", "luminoso", "neblina", "oscuros", "palmera", "tormenta",
	"valioso",
}

// weightedPassphraseWords is built at init time by repeating each word
// according to a bell-curve weight on its length:
//
//	3 letters → 1×   (rare)
//	4 letters → 3×
//	5 letters → 6×   (peak)
//	6 letters → 3×
//	7 letters → 1×   (rare)
var weightedPassphraseWords []string

func init() {
	weights := map[int]int{3: 1, 4: 3, 5: 6, 6: 3, 7: 1}
	seen := make(map[string]bool, len(passphraseWords))
	for _, w := range passphraseWords {
		if seen[w] {
			continue
		}
		seen[w] = true
		mult := weights[len(w)]
		for i := 0; i < mult; i++ {
			weightedPassphraseWords = append(weightedPassphraseWords, w)
		}
	}
}
