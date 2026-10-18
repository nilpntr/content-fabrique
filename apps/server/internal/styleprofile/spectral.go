package styleprofile

import "github.com/nilpntr/content-fabrique/internal/music"

// The spectral side of the sound lives in package music (the music analysis of a creator's track uses the same flux, tempo and flatness);
// these are its names here.

type spectrum = music.Spectrum

const maxSpectralSamples = music.MaxSpectralSamples

func pcmFromF32(raw []byte) []float32 { return music.PCMFromF32(raw) }

func analyseSpectrum(pcm []float32) spectrum { return music.AnalyseSpectrum(pcm) }

func tempoFromFlux(flux []float64) (bpm, conf, phaseMs float64) { return music.TempoFromFlux(flux) }
