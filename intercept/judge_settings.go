package intercept

import (
	"strconv"
	"strings"
)

// Apply in the original order and stop at the first failure. Values are resolved
// only when their write is reached; earlier successful writes are not rolled back.
func (i *Interceptor) writeJudgeSettings(c JudgeConfig) error {
	writes := []func() error{
		func() error { return i.db.SetBool(settingJudgeEnabled, c.Enabled) },
		func() error { return i.db.SetSetting(settingJudgeProfileID, strconv.FormatInt(c.ProfileID, 10)) },
		func() error {
			prompt := ""
			if strings.TrimSpace(c.Prompt) != "" && strings.TrimSpace(c.Prompt) != strings.TrimSpace(DefaultJudgePrompt) {
				prompt = c.Prompt
			}
			return i.db.SetSetting(settingJudgePrompt, prompt)
		},
		func() error { return i.db.SetSetting(settingJudgeTimeoutSecs, strconv.Itoa(c.TimeoutSeconds)) },
		func() error { return i.db.SetSetting(settingJudgeFailAction, c.FailAction) },
		func() error { return i.db.SetSetting(settingJudgeAskTimeoutSecs, strconv.Itoa(c.AskTimeoutSeconds)) },
		func() error { return i.db.SetSetting(settingJudgeAskTimeoutAction, c.AskTimeoutAction) },
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return err
		}
	}
	return nil
}
