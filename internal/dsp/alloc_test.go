package dsp

import "testing"

// The per-sample paths run 48 000 times a second and must not allocate.
func TestPerSampleProcessorsDoNotAllocate(t *testing.T) {
	clip, _ := NewClipFilter(DefaultClipLowpassHz)
	env := NewEnvelope(testFS)
	for name, fn := range map[string]func(){
		"ClipFilter": func() { clip.Process(0.1) },
		"Envelope":   func() { env.Process(0.1) },
	} {
		if a := testing.AllocsPerRun(10000, fn); a != 0 {
			t.Errorf("%s.Process allocates %v times per call, want 0", name, a)
		}
	}
}
