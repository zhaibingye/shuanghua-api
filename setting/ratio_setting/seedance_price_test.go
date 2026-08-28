package ratio_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultSeedanceSellingPricesUseOfficialMarkup(t *testing.T) {
	assert.InDelta(t, 69, defaultSeedancePrices["doubao-seedance-2-0-260128"].Text["720p"], 1e-9)
	assert.InDelta(t, 76.5, defaultSeedancePrices["doubao-seedance-2-0-260128"].Text["1080p"], 1e-9)
	assert.InDelta(t, 55.5, defaultSeedancePrices["doubao-seedance-2-0-fast-260128"].Text["720p"], 1e-9)
	assert.InDelta(t, 105, defaultSeedancePrices["doubao-seedance-2-5-260628"].Text["720p"], 1e-9)
}

func TestSeedanceTokensPerSecondUsesOfficial16x9Formula(t *testing.T) {
	assert.InDelta(t, 9720, SeedanceTokensPerSecond("480p"), 1e-9)
	assert.InDelta(t, 21600, SeedanceTokensPerSecond("720p"), 1e-9)
	assert.InDelta(t, 48600, SeedanceTokensPerSecond("1080p"), 1e-9)
	assert.InDelta(t, 194400, SeedanceTokensPerSecond("4k"), 1e-9)
}

func TestGetSeedanceUnitPriceRMBDefaults(t *testing.T) {
	price, ok := GetSeedanceUnitPriceRMB("doubao-seedance-2-0-260128", "1080p", false)
	require.True(t, ok)
	assert.InDelta(t, 76.5, price, 1e-9)

	price, ok = GetSeedanceUnitPriceRMB("doubao-seedance-2-0-260128", "1080p", true)
	require.True(t, ok)
	assert.InDelta(t, 46.5, price, 1e-9)
}

func TestLookupSeedanceUnitPricePrefersRequestNameThenUpstream(t *testing.T) {
	price, matched, ok := LookupSeedanceUnitPriceRMB("1080p", true, "my-seedance-alias", "doubao-seedance-2-0-260128")
	require.True(t, ok)
	assert.Equal(t, "doubao-seedance-2-0-260128", matched)
	assert.InDelta(t, 46.5, price, 1e-9)
}

func TestResolveSeedanceModelPrefixMatch(t *testing.T) {
	_, matched, ok := ResolveSeedanceModel("doubao-seedance-2-0-260128-se")
	require.True(t, ok)
	assert.Equal(t, "doubao-seedance-2-0-260128", matched)
}

func TestLoadSeedancePricesFromJSONStringOverridesDefaults(t *testing.T) {
	original := seedancePriceSetting.Prices
	t.Cleanup(func() {
		seedancePriceSetting.Prices = original
		RebuildSeedancePriceIndex()
	})

	LoadSeedancePricesFromJSONString(`{
		"my-seedance-alias": {
			"text": {"720p": 10, "1080p": 20},
			"video": {"720p": 5, "1080p": 8}
		}
	}`)

	price, ok := GetSeedanceUnitPriceRMB("my-seedance-alias", "1080p", false)
	require.True(t, ok)
	assert.InDelta(t, 20, price, 1e-9)

	_, ok = GetSeedanceUnitPriceRMB("doubao-seedance-2-0-260128", "1080p", false)
	assert.False(t, ok)
}

func TestLoadSeedancePricesEmptyFallsBackToDefaults(t *testing.T) {
	original := seedancePriceSetting.Prices
	t.Cleanup(func() {
		seedancePriceSetting.Prices = original
		RebuildSeedancePriceIndex()
	})

	LoadSeedancePricesFromJSONString("{}")
	price, ok := GetSeedanceUnitPriceRMB("doubao-seedance-2-0-260128", "1080p", false)
	require.True(t, ok)
	assert.InDelta(t, 76.5, price, 1e-9)
}

func TestEstimateSeedanceQuoteDirectAndSuperResolution(t *testing.T) {
	directQuota, directSnap, _, ok := EstimateSeedanceQuota(SeedanceQuoteInput{
		ModelNames:        []string{"doubao-seedance-2-0-260128"},
		BillingResolution: "720p",
		DurationSeconds:   5,
		GroupRatio:        1,
	})
	require.True(t, ok)
	assert.False(t, directSnap.SuperResolution)
	assert.Greater(t, directQuota, 0)

	srQuota, srSnap, _, ok := EstimateSeedanceQuota(SeedanceQuoteInput{
		ModelNames:        []string{"doubao-seedance-2-0-260128-se", "doubao-seedance-2-0-260128"},
		BillingResolution: "480p",
		OutputResolution:  "720p",
		DurationSeconds:   5,
		SuperResolution:   true,
		GroupRatio:        1,
	})
	require.True(t, ok)
	assert.True(t, srSnap.SuperResolution)
	assert.Equal(t, "480p", srSnap.BillingResolution)
	assert.Equal(t, "720p", srSnap.OutputResolution)
	assert.InDelta(t, 0.05, srSnap.SuperResolutionRMB, 1e-9)
	assert.Greater(t, srQuota, 0)

	directCost := SeedanceCostRMB(directSnap, 0, 5)
	srCost := SeedanceCostRMB(srSnap, 0, 5)
	assert.InDelta(t, SeedancePerSecondRMB(69, "720p")*5, directCost, 1e-9)
	assert.InDelta(t, SeedancePerSecondRMB(69, "480p")*5+0.05*5, srCost, 1e-9)
	assert.Less(t, srCost, directCost)
}

func TestSeedanceMediaKitPolicyAndIndependentSource(t *testing.T) {
	source, target, ok := SeedanceMediaKitPolicy("480p")
	require.True(t, ok)
	assert.Equal(t, "480p", source)
	assert.Equal(t, "720p", target)

	source, target, ok = SeedanceMediaKitPolicy("720p")
	require.True(t, ok)
	assert.Equal(t, "480p", source)
	assert.Equal(t, "1080p", target)

	source, target, ok = SeedanceMediaKitPolicy("1080p")
	require.True(t, ok)
	assert.Equal(t, "720p", source)
	assert.Equal(t, "1080p", target)

	_, _, ok = SeedanceMediaKitPolicy("4k")
	assert.False(t, ok)

	from480, ok := BuildSeedanceSnapshot(SeedanceQuoteInput{
		ModelNames:        []string{"doubao-seedance-2-0-260128"},
		BillingResolution: "480p",
		OutputResolution:  "1080p",
		SuperResolution:   true,
		DurationSeconds:   5,
	})
	require.True(t, ok)
	assert.Equal(t, "480p", from480.BillingResolution)
	assert.Equal(t, "1080p", from480.OutputResolution)
	assert.InDelta(t, 0.1, from480.SuperResolutionRMB, 1e-9)

	from720, ok := BuildSeedanceSnapshot(SeedanceQuoteInput{
		ModelNames:        []string{"doubao-seedance-2-0-260128"},
		BillingResolution: "720p",
		OutputResolution:  "1080p",
		SuperResolution:   true,
		DurationSeconds:   5,
	})
	require.True(t, ok)
	assert.Equal(t, "720p", from720.BillingResolution)
	assert.Equal(t, "1080p", from720.OutputResolution)
	assert.InDelta(t, 0.1, from720.SuperResolutionRMB, 1e-9)
	assert.Greater(t, SeedanceCostRMB(from720, 0, 5), SeedanceCostRMB(from480, 0, 5))
}

func TestSettleSeedanceQuoteUsesActualTokensAndInferredDuration(t *testing.T) {
	snap, ok := BuildSeedanceSnapshot(SeedanceQuoteInput{
		ModelNames:       []string{"doubao-seedance-2-0-260128"},
		OutputResolution: "1080p",
		SuperResolution:  true,
		HasVideo:         false,
		DurationSeconds:  5,
		GroupRatio:       1,
	})
	require.True(t, ok)
	assert.Equal(t, "720p", snap.BillingResolution)
	assert.Equal(t, "1080p", snap.OutputResolution)
	assert.InDelta(t, 0.1, snap.SuperResolutionRMB, 1e-9)

	tokens := int(snap.TokensPerSecond * 8)
	quota, _ := SettleSeedanceQuota(snap, tokens, 0, 1)
	assert.Greater(t, quota, 0)

	expectedRMB := float64(tokens)/1_000_000*snap.UnitPriceRMB + 8*snap.SuperResolutionRMB
	expectedQuota, _ := QuotaFromRMB(expectedRMB, 1)
	assert.Equal(t, expectedQuota, quota)
}

func TestQuotaFromRMBUsesSiteExchangeRate(t *testing.T) {
	original := operation_setting.USDExchangeRate
	t.Cleanup(func() {
		operation_setting.USDExchangeRate = original
	})

	cost := SeedancePerSecondRMB(69, "720p")
	operation_setting.USDExchangeRate = 7.3
	highRate, _ := QuotaFromRMB(cost, 1)
	operation_setting.USDExchangeRate = 1
	oneToOne, _ := QuotaFromRMB(cost, 1)

	assert.Greater(t, oneToOne, highRate)
	assert.InDelta(t, float64(highRate)*7.3, float64(oneToOne), 2)
}

func TestBuildSeedancePublicPricingMarksSuperResolutionOutputs(t *testing.T) {
	direct, ok := BuildSeedancePublicPricing([]string{"doubao-seedance-2-0-260128"}, false)
	require.True(t, ok)
	assert.False(t, direct.SuperResolution)
	assert.InDelta(t, SeedancePerSecondRMB(69, "720p"), direct.TextPerSecondRMB["720p"], 1e-9)

	sr, ok := BuildSeedancePublicPricing([]string{"doubao-seedance-2-0-260128-se"}, true)
	require.True(t, ok)
	assert.True(t, sr.SuperResolution)
	assert.InDelta(t, 0.05, sr.SRFrom480To720RMB, 1e-9)
	assert.InDelta(t, 0.1, sr.SRFrom720To1080RMB, 1e-9)
	assert.InDelta(t, SeedancePerSecondRMB(69, "480p")+0.05, sr.OutputTextPerSecondRMB["480p"], 1e-9)
	assert.InDelta(t, SeedancePerSecondRMB(69, "480p")+0.1, sr.OutputTextPerSecondRMB["720p"], 1e-9)
	assert.InDelta(t, SeedancePerSecondRMB(69, "720p")+0.1, sr.OutputTextPerSecondRMB["1080p"], 1e-9)
}

func TestLoadSeedanceSuperResolutionMigratesLegacyOfficialPair(t *testing.T) {
	original := seedancePriceSetting.SuperResolution
	t.Cleanup(func() {
		seedancePriceSetting.SuperResolution = original
		RebuildSeedancePriceIndex()
	})

	LoadSeedanceSuperResolutionFromJSONString(`{"480_to_720":0.02,"720_to_1080":0.04}`)
	sr := CurrentSeedanceSuperResolution()
	assert.InDelta(t, 0.05, sr.From480To720, 1e-9)
	assert.InDelta(t, 0.1, sr.From720To1080, 1e-9)
}

func TestLoadSeedanceSuperResolutionKeepsCustomPrices(t *testing.T) {
	original := seedancePriceSetting.SuperResolution
	t.Cleanup(func() {
		seedancePriceSetting.SuperResolution = original
		RebuildSeedancePriceIndex()
	})

	LoadSeedanceSuperResolutionFromJSONString(`{"480_to_720":0.03,"720_to_1080":0.08}`)
	sr := CurrentSeedanceSuperResolution()
	assert.InDelta(t, 0.03, sr.From480To720, 1e-9)
	assert.InDelta(t, 0.08, sr.From720To1080, 1e-9)
}
