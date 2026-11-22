package mesh

import (
	"errors"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/awgparams/awgtest"
	"github.com/tumour/awg-mesh/internal/state"
)

// AdoptPending — единственное решение «принять ли Pending» для ОБОИХ каналов доставки
// (gossip-pull и push от seed). Невалидный для device набор не принимается НИКОГДА:
// иначе флиппер запишет его в state.AwgParams, и нода не поднимет awg0 на следующем
// рестарте (а без ack на него seed не закоммитит flag-day — mesh останется согласованным).

func pend(v uint64, applyAt time.Time, p awgparams.Params) *state.PendingParams {
	return &state.PendingParams{Version: v, ApplyAt: applyAt, Params: p}
}

func TestAdoptPending_VersionLogicUnchangedForValidSets(t *testing.T) {
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	ok := awgtest.Valid(16)
	cases := []struct {
		name          string
		cur           uint64
		local, remote *state.PendingParams
		want          bool
	}{
		{"nil remote", 4, nil, nil, false},
		{"newer than applied, no local", 4, nil, pend(5, time.Time{}, ok), true},
		{"not newer than applied", 5, nil, pend(5, at, ok), false},
		{"older than applied", 6, nil, pend(5, at, ok), false},
		{"newer than local", 4, pend(5, time.Time{}, ok), pend(6, time.Time{}, ok), true},
		{"older than local", 4, pend(6, time.Time{}, ok), pend(5, at, ok), false},
		{"same version commit over announce", 4, pend(5, time.Time{}, ok), pend(5, at, ok), true},
		{"same version announce over commit", 4, pend(5, at, ok), pend(5, time.Time{}, ok), false},
		{"same version commit repeat", 4, pend(5, at, ok), pend(5, at, ok), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := AdoptPending(c.cur, c.local, c.remote)
			if err != nil {
				t.Fatalf("валидный набор дал ошибку: %v", err)
			}
			if got != c.want {
				t.Fatalf("adopt = %v, want %v", got, c.want)
			}
			if got != ShouldAdoptPending(c.cur, c.local, c.remote) {
				t.Fatal("для валидного набора решение обязано совпадать с ShouldAdoptPending")
			}
		})
	}
}

// Набор, который device отвергнет, не принимается ни как новый анонс, ни как commit
// поверх уже принятого анонса — и это ошибка (каналу есть что сказать seed'у).
func TestAdoptPending_RejectsDeviceInvalid(t *testing.T) {
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, base := range []awgparams.Params{awgtest.Valid(16), awgtest.Legacy()} {
		for name, bad := range awgtest.DeviceInvalid(base) {
			t.Run(name+"/announce", func(t *testing.T) {
				got, err := AdoptPending(4, nil, pend(5, time.Time{}, bad))
				if got || !errors.Is(err, ErrPendingRejected) {
					t.Fatalf("adopt=%v err=%v, want false + ErrPendingRejected", got, err)
				}
			})
			t.Run(name+"/commit-over-announce", func(t *testing.T) {
				got, err := AdoptPending(4, pend(5, time.Time{}, base), pend(5, at, bad))
				if got || !errors.Is(err, ErrPendingRejected) {
					t.Fatalf("adopt=%v err=%v, want false + ErrPendingRejected", got, err)
				}
			})
		}
	}
}

// Устаревший Pending отвергается по версии, а не по содержимому: на идемпотентный повтор
// старого (даже кривого) набора ошибки нет — seed не должен видеть ложный отказ.
func TestAdoptPending_StaleInvalidIsNotAnError(t *testing.T) {
	for name, bad := range awgtest.DeviceInvalid(awgtest.Valid(16)) {
		t.Run(name, func(t *testing.T) {
			got, err := AdoptPending(5, nil, pend(5, time.Time{}, bad))
			if got || err != nil {
				t.Fatalf("adopt=%v err=%v, want false без ошибки (не новее применённого)", got, err)
			}
		})
	}
}

// Не строже device: набор вне политики, но приемлемый для библиотеки, принимается.
func TestAdoptPending_AcceptsOutsidePolicyDeviceValid(t *testing.T) {
	for name, p := range awgtest.OutsidePolicyDeviceValid(awgtest.Valid(16)) {
		t.Run(name, func(t *testing.T) {
			got, err := AdoptPending(4, nil, pend(5, time.Time{}, p))
			if !got || err != nil {
				t.Fatalf("adopt=%v err=%v, want true", got, err)
			}
		})
	}
}
