package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The transcription providers TRANSCRIBE_PROVIDER accepts.
const (
	TranscribeWhisperCPP = "whispercpp"
	TranscribeWorkersAI  = "workersai"
)

// Defaults of the transcription settings.
const (
	DefaultTranscribeModel       = "large-v3-turbo-q5_0"
	DefaultTranscribeConcurrency = 1
	maxTranscribeConcurrency     = 8
)

var transcribeLanguageRE = regexp.MustCompile(`^[a-z]{2,3}$`)

// loadTranscribe reads and checks the transcription settings (D-27): whisper.cpp in the sandbox locally, Workers AI when hosted.
// A Workers AI provider without an account is not an error: the transcribe jobs wait until it has one (staging has none yet, P0-09).
func (c *Config) loadTranscribe(getenv Getenv) []error {
	var errs []error
	c.TranscribeProvider = strings.ToLower(strings.TrimSpace(getenv("TRANSCRIBE_PROVIDER")))
	if c.TranscribeProvider == "" {
		c.TranscribeProvider = TranscribeWhisperCPP
		if c.Env == EnvStaging || c.Env == EnvProduction {
			c.TranscribeProvider = TranscribeWorkersAI
		}
	}
	switch c.TranscribeProvider {
	case TranscribeWhisperCPP, TranscribeWorkersAI:
	default:
		errs = append(errs, fmt.Errorf("TRANSCRIBE_PROVIDER: %q is not one of whispercpp, workersai", c.TranscribeProvider))
	}
	c.TranscribeModel = strings.TrimSpace(getenv("TRANSCRIBE_MODEL"))
	if c.TranscribeModel == "" {
		c.TranscribeModel = DefaultTranscribeModel
		if c.Env == EnvCI {
			c.TranscribeModel = "tiny.en"
		}
	}
	switch c.TranscribeModel {
	case "large-v3-turbo-q5_0", "tiny.en":
	default:
		errs = append(errs, fmt.Errorf("TRANSCRIBE_MODEL: %q is not one of large-v3-turbo-q5_0, tiny.en", c.TranscribeModel))
	}
	c.TranscribeLanguage = strings.ToLower(strings.TrimSpace(getenv("TRANSCRIBE_LANGUAGE")))
	if c.TranscribeLanguage == "auto" {
		c.TranscribeLanguage = ""
	}
	if c.TranscribeLanguage != "" && !transcribeLanguageRE.MatchString(c.TranscribeLanguage) {
		errs = append(errs, errors.New("TRANSCRIBE_LANGUAGE: must be empty (detect) or an ISO 639-1 code such as en or nl"))
	}
	c.TranscribeConcurrency = DefaultTranscribeConcurrency
	if raw := strings.TrimSpace(getenv("TRANSCRIBE_CONCURRENCY")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxTranscribeConcurrency {
			errs = append(errs, fmt.Errorf("TRANSCRIBE_CONCURRENCY: must be a whole number from 1 to %d", maxTranscribeConcurrency))
		} else {
			c.TranscribeConcurrency = n
		}
	}
	c.WhisperModelsDir = strings.TrimSpace(getenv("WHISPER_MODELS_DIR"))
	switch {
	case c.WhisperModelsDir == "":
		c.WhisperModelsDir = filepath.Join(c.MediaTempDir, "whisper-models")
	case !filepath.IsAbs(c.WhisperModelsDir):
		errs = append(errs, errors.New("WHISPER_MODELS_DIR: must be an absolute path"))
	}
	c.WorkersAIAccountID = strings.TrimSpace(getenv("WORKERS_AI_ACCOUNT_ID"))
	c.WorkersAIAPIToken = strings.TrimSpace(getenv("WORKERS_AI_API_TOKEN"))
	if raw := strings.TrimSpace(getenv("WORKERS_AI_PRICE_MICROS_PER_MINUTE")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 || n > 1_000_000 {
			errs = append(errs, errors.New("WORKERS_AI_PRICE_MICROS_PER_MINUTE: must be a whole number of millionths of a dollar from 0 to 1000000"))
		} else {
			c.WorkersAIPriceMicrosPerMinute = n
		}
	}
	if (c.WorkersAIAccountID == "") != (c.WorkersAIAPIToken == "") {
		errs = append(errs, errors.New("WORKERS_AI_ACCOUNT_ID and WORKERS_AI_API_TOKEN: set both or neither"))
	}
	return errs
}
