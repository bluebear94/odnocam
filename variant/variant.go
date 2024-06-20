package variant

type Variant string

const (
	VarClassic  Variant = "classic"
	VarWordSmog Variant = "wordsmog"
	// Redundant information, but we are deciding to treat different board
	// layouts as different variants.
	VarClassicSuper  Variant = "classic_super"
	VarWordSmogSuper Variant = "wordsmog_super"
	// Anadrome variant
	VarGmo Variant = "gmowords"
)

func (v Variant) GetBingoBonus() int {
	if v == VarGmo {
		return 35
	}
	return 50
}
