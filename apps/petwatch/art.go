package petwatch

// ASCII art frames for each lifecycle stage and mood. Everything is plain
// ASCII so it renders identically across every theme, including
// monochrome/plain color modes. Blocks clip rather than wrap, so each
// frame stays narrow and short (well under the 32-line block budget).

const artEgg = `    _____
   /     \
  |       |
  |       |
   \_____/`

// artEggCracked shows once the egg is most of the way to hatching.
const artEggCracked = `    _____
   /  \  \
  | /\    |
  |   \/  |
   \______/`

const artChickHappy = `   .-""-.
  ( o.o )
  (  v  )
   '-.-'`

const artChickContent = `   .-""-.
  ( o o )
  (  _  )
   '-.-'`

const artChickSad = `   .-""-.
  ( u u )
  (  ~  )
   '-.-'`

const artChickSick = `   .-""-.
  ( x x )
  (  ~  )
   '-.-'`

const artChickAsleep = `   .-""-.   z
  ( - - )  z
  (  u  )
   '-.-'`

const artChickEating = `   .-""-.
  ( o.o )
  (  @  )
   '-.-'  nom`

const artAdultHappy = `   .-""""-.
  /        \
 |  ^    ^  |
 |   \__/   |
  \        /
   '-....-'`

const artAdultContent = `   .-""""-.
  /        \
 |  o    o  |
 |    --    |
  \        /
   '-....-'`

const artAdultSad = `   .-""""-.
  /        \
 |  u    u  |
 |    ~     |
  \        /
   '-....-'`

const artAdultSick = `   .-""""-.
  /        \
 |  x    x  |
 |    ~~~   |
  \        /
   '-....-'`

const artAdultAsleep = `   .-""""-.    z
  /        \  z
 |  -    -  |
 |    __    |
  \        /
   '-....-'`

const artAdultEating = `   .-""""-.
  /        \
 |  o    o  |
 |   \@@/   |
  \        /
   '-....-'  nom`

const artAdultPlaying = `   .-""""-.
  /  ^  ^  \
 |  o    o  |
 |   \__/   |
  \        /
   '-....-'  *whee*`

// artFor picks the frame for the pet's stage and mood.
func artFor(stage Stage, mood Mood) string {
	if stage == StageEgg {
		return artEgg
	}
	switch stage {
	case StageChick:
		switch mood {
		case MoodAsleep:
			return artChickAsleep
		case MoodEating:
			return artChickEating
		case MoodSick:
			return artChickSick
		case MoodSad:
			return artChickSad
		case MoodHappy, MoodPlaying:
			return artChickHappy
		default:
			return artChickContent
		}
	default:
		switch mood {
		case MoodAsleep:
			return artAdultAsleep
		case MoodEating:
			return artAdultEating
		case MoodSick:
			return artAdultSick
		case MoodSad:
			return artAdultSad
		case MoodPlaying:
			return artAdultPlaying
		case MoodHappy:
			return artAdultHappy
		default:
			return artAdultContent
		}
	}
}

// faceLine is the one-line fallback face used when the negotiated
// presentation version does not carry blocks.
func faceLine(mood Mood) string {
	switch mood {
	case MoodAsleep:
		return "(-.-) zZ"
	case MoodSick:
		return "(x~x)"
	case MoodSad:
		return "(u_u)"
	case MoodEating:
		return "(o@o) nom"
	case MoodPlaying:
		return "\\(^o^)/"
	case MoodHappy:
		return "(^o^)"
	default:
		return "(o_o)"
	}
}
