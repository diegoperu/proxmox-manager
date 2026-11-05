package handlers

import "testing"

func TestGPUMemoryGB(t *testing.T) {
	for name, want := range map[string]int{
		"NVIDIA Corporation GA107GL [A2 / A16]":                                         16,
		"NVIDIA Corporation GA100 [A100 SXM4 40GB]":                                     40,
		"NVIDIA Corporation TU104GL [Tesla T4]":                                         16,
		"NVIDIA Corporation AD104GL [L4]":                                               24,
		"NVIDIA Corporation GA102GL [A10]":                                              24,
		"NVIDIA Corporation GH100 [H100L 94GB]":                                         94,
		"NVIDIA Corporation GB202GL [RTX PRO 6000 Blackwell Max-Q Workstation Edition]": 96,
		// not in the table and no size in the name: unknown, never guessed (A100 must not match "A10")
		"NVIDIA Corporation GA100 [A100 PCIe]":  0,
		"NVIDIA Corporation Something":          0,
		"":                                      0,
		"NVIDIA Corporation X [A2 / A16] extra": 0,
		"NVIDIA 9999 GB":                        0, // out of range
	} {
		if got := gpuMemoryGB(name); got != want {
			t.Errorf("%q: %d, want %d", name, got, want)
		}
	}
}
