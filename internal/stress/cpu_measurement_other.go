//go:build !linux

package stress

func platformProcessCPUSampler() processCPUSampler {
	return nil
}
