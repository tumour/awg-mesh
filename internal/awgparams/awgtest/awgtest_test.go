package awgtest

import (
	"testing"

	"github.com/tumour/awg-mesh/internal/awgparams"
)

// Фикстуры обязаны быть тем, чем названы, — иначе все тесты на них доказывают не то.
// (Совпадение с НАСТОЯЩИМ device проверяет internal/wg: TestAwgtestFixturesMatchDevice.)

func TestValidPassesPolicyAndDevice(t *testing.T) {
	for _, s4 := range []int{0, 7, 16, 65535} {
		p := Valid(s4)
		if err := awgparams.Validate(p); err != nil {
			t.Errorf("Valid(%d) не проходит политику: %v", s4, err)
		}
		if err := awgparams.CheckDeviceLimits(p); err != nil {
			t.Errorf("Valid(%d) не проходит лимиты device: %v", s4, err)
		}
	}
}

func TestDeviceInvalidFailsDeviceLimits(t *testing.T) {
	for _, base := range []awgparams.Params{Valid(16), Legacy()} {
		for name, p := range DeviceInvalid(base) {
			if awgparams.CheckDeviceLimits(p) == nil {
				t.Errorf("DeviceInvalid[%s] проходит CheckDeviceLimits: %+v", name, p)
			}
		}
	}
}

func TestOutsidePolicyDeviceValidIsExactlyThat(t *testing.T) {
	for name, p := range OutsidePolicyDeviceValid(Valid(16)) {
		if awgparams.Validate(p) == nil {
			t.Errorf("OutsidePolicyDeviceValid[%s] проходит политику — фикстура ничего не проверяет", name)
		}
		if err := awgparams.CheckDeviceLimits(p); err != nil {
			t.Errorf("OutsidePolicyDeviceValid[%s] не проходит лимиты device: %v", name, err)
		}
	}
}
