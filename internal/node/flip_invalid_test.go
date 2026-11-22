package node

import (
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/awgparams/awgtest"
	"github.com/tumour/awg-mesh/internal/state"
)

// Pending, который device отвергнет, мог попасть в state ДО апгрейда (его принял старый
// бинарь без проверки). Когда он наступит, флиппер НЕ должен писать его в
// state.AwgParams: на следующем рестарте Configure не поднимет awg0, и нода без
// out-of-band потеряна навсегда — watchdog self-upgrade этого уже не увидит.
// Правильное поведение: набор не трогать, device не трогать, Pending снять (иначе
// флиппер будет спотыкаться о него каждый тик), версию не поднимать.
func TestApplyIfDue_DeviceInvalidPendingNeverPersisted(t *testing.T) {
	now := time.Now().UTC()
	applyAt := now.Add(-time.Minute)
	for name, bad := range awgtest.DeviceInvalid(awgtest.Valid(16)) {
		t.Run(name, func(t *testing.T) {
			store := storeWithPending(t, &state.PendingParams{Params: bad, Version: 2, ApplyAt: applyAt})
			old, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			dev := &fakeDevice{}
			f := flipperFor(store, dev, time.Minute, time.Hour, 2, applyAt.Add(-time.Hour))

			f.applyIfDue(now)

			if dev.appliedParams {
				t.Error("кривой набор отправлен в device.ApplyParams")
			}
			s, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if s.AwgParams != old.AwgParams {
				t.Errorf("кривой набор записан в state.AwgParams: %+v", s.AwgParams)
			}
			if s.ParamsVersion != old.ParamsVersion {
				t.Errorf("ParamsVersion = %d, want %d (набор не применён)", s.ParamsVersion, old.ParamsVersion)
			}
			if s.Pending != nil {
				t.Errorf("кривой Pending не снят: %+v", s.Pending)
			}
			// Главное: state после тика стартует — device примет его AwgParams.
			if err := awgparams.CheckDeviceLimits(s.AwgParams); err != nil {
				t.Errorf("state после тика не стартует: %v", err)
			}
		})
	}
}

// Повторные тики после снятия кривого Pending — no-op (ничего не флипают, не пишут).
func TestApplyIfDue_DeviceInvalidPendingThenIdle(t *testing.T) {
	now := time.Now().UTC()
	applyAt := now.Add(-time.Minute)
	bad := awgtest.DeviceInvalid(awgtest.Valid(16))["jmin>jmax"]
	store := storeWithPending(t, &state.PendingParams{Params: bad, Version: 2, ApplyAt: applyAt})
	dev := &fakeDevice{}
	f := flipperFor(store, dev, time.Minute, time.Hour, 2, applyAt.Add(-time.Hour))
	for i := 0; i < 3; i++ {
		f.applyIfDue(now.Add(time.Duration(i) * time.Second))
	}
	if dev.appliedParams {
		t.Fatal("device тронут")
	}
	if s, _ := store.Read(); s.Pending != nil || s.ParamsVersion != 1 {
		t.Fatalf("state после повторных тиков: v=%d pending=%+v", s.ParamsVersion, s.Pending)
	}
}

// Не строже device: due-Pending вне политики, но приемлемый для библиотеки (легаси-H,
// J=0, …) применяется, как и раньше.
func TestApplyIfDue_OutsidePolicyDeviceValidApplied(t *testing.T) {
	now := time.Now().UTC()
	applyAt := now.Add(-time.Minute)
	for name, p := range awgtest.OutsidePolicyDeviceValid(awgtest.Valid(16)) {
		t.Run(name, func(t *testing.T) {
			store := storeWithPending(t, &state.PendingParams{Params: p, Version: 2, ApplyAt: applyAt})
			dev := &fakeDevice{}
			f := flipperFor(store, dev, time.Minute, time.Hour, 2, applyAt.Add(-time.Hour))
			f.applyIfDue(now)
			if !dev.appliedParams {
				t.Error("device.ApplyParams не вызван")
			}
			if s, _ := store.Read(); s.AwgParams != p || s.ParamsVersion != 2 || s.Pending != nil {
				t.Errorf("набор не применён: v=%d params=%+v pending=%+v", s.ParamsVersion, s.AwgParams, s.Pending)
			}
		})
	}
}
