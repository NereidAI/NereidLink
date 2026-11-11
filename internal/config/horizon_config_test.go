// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

func TestHorizonDisabledIsValidAndIgnoresFields(t *testing.T) {
	cfg := &Config{Horizon: HorizonSection{}}
	if err := cfg.validateHorizon(); err != nil {
		t.Fatalf("disabled integration must validate without configuration: %v", err)
	}
}

func TestHorizonEnabledRequiresBaseURLOrgAndKey(t *testing.T) {
	base := func() HorizonSection {
		return HorizonSection{
			Enabled: true, BaseURL: "https://nh.example", Org: "yiqisoft",
			APIKey: "nk_service", GatewayDestinations: map[string]string{
				"gateway-1": "openhorizon.edgenode:node1",
			},
		}
	}
	cases := map[string]func(HorizonSection) HorizonSection{
		"missing base url": func(h HorizonSection) HorizonSection { h.BaseURL = ""; return h },
		"root org":         func(h HorizonSection) HorizonSection { h.Org = "root"; return h },
		"empty org":        func(h HorizonSection) HorizonSection { h.Org = ""; return h },
		"missing api key":  func(h HorizonSection) HorizonSection { h.APIKey = ""; return h },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{Horizon: mutate(base())}
			if err := cfg.validateHorizon(); err == nil {
				t.Fatal("expected startup validation failure")
			}
		})
	}
	if err := (&Config{Horizon: base()}).validateHorizon(); err != nil {
		t.Fatalf("complete enabled section must validate: %v", err)
	}
}

func TestHorizonRejectsInsecureWithCAAndBadMappings(t *testing.T) {
	valid := HorizonSection{
		Enabled: true, BaseURL: "https://nh.example", Org: "yiqisoft", APIKey: "k",
	}
	badCA := valid
	badCA.InsecureSkipVerify = true
	badCA.CAFile = "ca.pem"
	if err := (&Config{Horizon: badCA}).validateHorizon(); err == nil {
		t.Fatal("insecure_skip_verify plus ca_file must be rejected")
	}

	badPrecision := valid
	badPrecision.DefaultPrecision = "int4"
	if err := (&Config{Horizon: badPrecision}).validateHorizon(); err == nil {
		t.Fatal("invalid default precision must be rejected")
	}

	badMapping := valid
	badMapping.GatewayDestinations = map[string]string{"gateway-1": "node1-no-colon"}
	if err := (&Config{Horizon: badMapping}).validateHorizon(); err == nil {
		t.Fatal("destination token without a type:id colon must be rejected")
	}

	goodMapping := valid
	goodMapping.GatewayDestinations = map[string]string{"gateway-1": "openhorizon.edgenode:node1"}
	if err := (&Config{Horizon: goodMapping}).validateHorizon(); err != nil {
		t.Fatalf("valid mapping rejected: %v", err)
	}
}

func TestHorizonPrecisionOrDefault(t *testing.T) {
	section := HorizonSection{}
	if got, err := section.PrecisionOrDefault(""); err != nil || got != HorizonPrecisionFP16 {
		t.Fatalf("empty precision should default to fp16, got %q err=%v", got, err)
	}
	section.DefaultPrecision = HorizonPrecisionINT8
	if got, _ := section.PrecisionOrDefault(""); got != HorizonPrecisionINT8 {
		t.Fatalf("section default not applied: %q", got)
	}
	if got, _ := section.PrecisionOrDefault("fp16"); got != HorizonPrecisionFP16 {
		t.Fatalf("explicit precision must beat the default: %q", got)
	}
	if _, err := section.PrecisionOrDefault("int4"); err == nil {
		t.Fatal("invalid precision must error")
	}
}
