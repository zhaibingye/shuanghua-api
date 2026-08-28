package ratio_setting

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
)

const (
	SeedancePriceOptionKey           = "seedance_price_setting.prices"
	SeedanceSuperResolutionOptionKey = "seedance_price_setting.super_resolution"

	// SeedanceDefaultDurationSeconds is the pre-consume fallback when duration
	// is omitted or adaptive (duration=-1).
	SeedanceDefaultDurationSeconds = 5
	SeedanceFrameRate              = 24
	seedanceSellingMarkup          = 1.5
)

// SeedanceModelPrice stores selling prices in RMB per million tokens.
// Text is the no-video-input bucket; Video is used when the request includes video.
type SeedanceModelPrice struct {
	Text  map[string]float64 `json:"text"`
	Video map[string]float64 `json:"video"`
}

// SeedanceSuperResolutionPrice stores MediaKit enhance-video list prices in RMB per second of FINAL output.
// JSON keys stay 480_to_720 / 720_to_1080 for compatibility.
// From480To720 is the 720p-final rate (client 480p: generate 480p, enhance to 720p).
// From720To1080 is the 1080p-final rate (client 720p: 480→1080 and client 1080p: 720→1080).
type SeedanceSuperResolutionPrice struct {
	From480To720  float64 `json:"480_to_720"`
	From720To1080 float64 `json:"720_to_1080"`
}

type SeedancePriceSetting struct {
	Prices          map[string]SeedanceModelPrice `json:"prices"`
	SuperResolution SeedanceSuperResolutionPrice  `json:"super_resolution"`
}

type seedanceRuntime struct {
	Prices          map[string]SeedanceModelPrice
	SuperResolution SeedanceSuperResolutionPrice
}

// officialSeedancePrices is the Volcengine Ark list price in RMB / million tokens.
var officialSeedancePrices = map[string]SeedanceModelPrice{
	"doubao-seedance-2-0-260128": {
		Text:  map[string]float64{"480p": 46, "720p": 46, "1080p": 51, "4k": 26},
		Video: map[string]float64{"480p": 28, "720p": 28, "1080p": 31, "4k": 16},
	},
	"doubao-seedance-2-0-fast-260128": {
		Text:  map[string]float64{"480p": 37, "720p": 37},
		Video: map[string]float64{"480p": 22, "720p": 22},
	},
	"doubao-seedance-2-5-260628": {
		Text:  map[string]float64{"480p": 70, "720p": 70},
		Video: map[string]float64{"480p": 42, "720p": 42},
	},
}

// Default MediaKit enhance-video selling prices (RMB / final-output second).
// 720p-final = 0.05; 1080p-final (480→1080 and 720→1080) = 0.1.
var officialSeedanceSuperResolution = SeedanceSuperResolutionPrice{
	From480To720:  0.05,
	From720To1080: 0.1,
}

// legacyOfficialSeedanceSuperResolution is the previous official pair. Stored
// configs that still equal this pair are migrated to the new defaults.
var legacyOfficialSeedanceSuperResolution = SeedanceSuperResolutionPrice{
	From480To720:  0.02,
	From720To1080: 0.04,
}

var defaultSeedancePrices = scaleSeedancePrices(officialSeedancePrices, seedanceSellingMarkup)

var seedancePriceSetting = SeedancePriceSetting{
	Prices:          cloneSeedancePrices(defaultSeedancePrices),
	SuperResolution: officialSeedanceSuperResolution,
}

var seedanceRuntimeIndex atomic.Value

// 16:9 pixel sizes used to estimate tokens/second. Actual settlement uses
// upstream usage tokens; this table is for pre-consume and price display.
var seedanceFrameSize = map[string][2]int{
	"480p":  {864, 480},
	"720p":  {1280, 720},
	"1080p": {1920, 1080},
	"4k":    {3840, 2160},
}

func init() {
	config.GlobalConfig.Register("seedance_price_setting", &seedancePriceSetting)
	RebuildSeedancePriceIndex()
}

func scaleSeedancePrices(src map[string]SeedanceModelPrice, scale float64) map[string]SeedanceModelPrice {
	dst := make(map[string]SeedanceModelPrice, len(src))
	for modelName, price := range src {
		dst[modelName] = SeedanceModelPrice{
			Text:  scaleFloatMap(price.Text, scale),
			Video: scaleFloatMap(price.Video, scale),
		}
	}
	return dst
}

func scaleFloatMap(src map[string]float64, scale float64) map[string]float64 {
	dst := make(map[string]float64, len(src))
	for key, value := range src {
		dst[key] = value * scale
	}
	return dst
}

func cloneSeedancePrices(src map[string]SeedanceModelPrice) map[string]SeedanceModelPrice {
	dst := make(map[string]SeedanceModelPrice, len(src))
	for modelName, price := range src {
		dst[modelName] = SeedanceModelPrice{
			Text:  cloneFloatMap(price.Text),
			Video: cloneFloatMap(price.Video),
		}
	}
	return dst
}

func cloneFloatMap(src map[string]float64) map[string]float64 {
	if len(src) == 0 {
		return map[string]float64{}
	}
	dst := make(map[string]float64, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func DefaultSeedancePrices() map[string]SeedanceModelPrice {
	return cloneSeedancePrices(defaultSeedancePrices)
}

func DefaultSeedancePricesJSON() string {
	bytes, err := common.Marshal(defaultSeedancePrices)
	if err != nil {
		common.SysError("error marshalling default seedance prices: " + err.Error())
		return "{}"
	}
	return string(bytes)
}

func DefaultSeedanceSuperResolution() SeedanceSuperResolutionPrice {
	return officialSeedanceSuperResolution
}

func DefaultSeedanceSuperResolutionJSON() string {
	bytes, err := common.Marshal(officialSeedanceSuperResolution)
	if err != nil {
		common.SysError("error marshalling default seedance super-resolution prices: " + err.Error())
		return "{}"
	}
	return string(bytes)
}

func ValidateSeedancePricesJSON(value string) error {
	_, err := decodeSeedancePricesJSON(value)
	return err
}

func ValidateSeedanceSuperResolutionJSON(value string) error {
	_, err := decodeSeedanceSuperResolutionJSON(value)
	return err
}

func LoadSeedancePricesFromJSONString(value string) {
	prices, err := decodeSeedancePricesJSON(value)
	if err != nil {
		common.SysError("加载 Seedance 价格失败，将使用默认价格表: " + err.Error())
		prices = cloneSeedancePrices(defaultSeedancePrices)
	}
	if len(prices) == 0 {
		prices = cloneSeedancePrices(defaultSeedancePrices)
	}
	seedancePriceSetting.Prices = prices
	RebuildSeedancePriceIndex()
}

func LoadSeedanceSuperResolutionFromJSONString(value string) {
	price, err := decodeSeedanceSuperResolutionJSON(value)
	if err != nil {
		common.SysError("加载 Seedance 超分价格失败，将使用默认价格: " + err.Error())
		price = officialSeedanceSuperResolution
	}
	seedancePriceSetting.SuperResolution = price
	RebuildSeedancePriceIndex()
}

func RebuildSeedancePriceIndex() {
	prices := seedancePriceSetting.Prices
	if len(prices) == 0 {
		prices = defaultSeedancePrices
	}
	sr := migrateLegacySeedanceSuperResolution(seedancePriceSetting.SuperResolution)
	if sr.From480To720 <= 0 && sr.From720To1080 <= 0 {
		sr = officialSeedanceSuperResolution
	}
	seedanceRuntimeIndex.Store(seedanceRuntime{
		Prices:          cloneSeedancePrices(prices),
		SuperResolution: sr,
	})
	InvalidateExposedDataCache()
}

func currentSeedanceRuntime() seedanceRuntime {
	value := seedanceRuntimeIndex.Load()
	if value == nil {
		RebuildSeedancePriceIndex()
		value = seedanceRuntimeIndex.Load()
	}
	runtime, _ := value.(seedanceRuntime)
	if len(runtime.Prices) == 0 {
		runtime.Prices = defaultSeedancePrices
	}
	runtime.SuperResolution = migrateLegacySeedanceSuperResolution(runtime.SuperResolution)
	if runtime.SuperResolution.From480To720 <= 0 && runtime.SuperResolution.From720To1080 <= 0 {
		runtime.SuperResolution = officialSeedanceSuperResolution
	}
	return runtime
}

func currentSeedancePrices() map[string]SeedanceModelPrice {
	return currentSeedanceRuntime().Prices
}

func decodeSeedancePricesJSON(value string) (map[string]SeedanceModelPrice, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "null" {
		return map[string]SeedanceModelPrice{}, nil
	}
	var prices map[string]SeedanceModelPrice
	if err := json.Unmarshal([]byte(trimmed), &prices); err != nil {
		return nil, fmt.Errorf("seedance prices must be a JSON object: %w", err)
	}
	if prices == nil {
		return map[string]SeedanceModelPrice{}, nil
	}
	normalized := make(map[string]SeedanceModelPrice, len(prices))
	for modelName, price := range prices {
		name := strings.TrimSpace(modelName)
		if name == "" {
			return nil, fmt.Errorf("seedance model name cannot be empty")
		}
		text, err := normalizeSeedancePriceMap(price.Text)
		if err != nil {
			return nil, fmt.Errorf("model %s text prices: %w", name, err)
		}
		video, err := normalizeSeedancePriceMap(price.Video)
		if err != nil {
			return nil, fmt.Errorf("model %s video prices: %w", name, err)
		}
		normalized[name] = SeedanceModelPrice{Text: text, Video: video}
	}
	return normalized, nil
}

func decodeSeedanceSuperResolutionJSON(value string) (SeedanceSuperResolutionPrice, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "null" {
		return officialSeedanceSuperResolution, nil
	}
	var price SeedanceSuperResolutionPrice
	if err := json.Unmarshal([]byte(trimmed), &price); err != nil {
		return SeedanceSuperResolutionPrice{}, fmt.Errorf("seedance super-resolution prices must be a JSON object: %w", err)
	}
	if !isFiniteNonNegative(price.From480To720) {
		return SeedanceSuperResolutionPrice{}, fmt.Errorf("invalid 480_to_720 price")
	}
	if !isFiniteNonNegative(price.From720To1080) {
		return SeedanceSuperResolutionPrice{}, fmt.Errorf("invalid 720_to_1080 price")
	}
	if price.From480To720 == 0 && price.From720To1080 == 0 {
		return officialSeedanceSuperResolution, nil
	}
	return migrateLegacySeedanceSuperResolution(price), nil
}

func migrateLegacySeedanceSuperResolution(price SeedanceSuperResolutionPrice) SeedanceSuperResolutionPrice {
	if almostEqual(price.From480To720, legacyOfficialSeedanceSuperResolution.From480To720) &&
		almostEqual(price.From720To1080, legacyOfficialSeedanceSuperResolution.From720To1080) {
		return officialSeedanceSuperResolution
	}
	return price
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func normalizeSeedancePriceMap(src map[string]float64) (map[string]float64, error) {
	dst := make(map[string]float64, len(src))
	for key, value := range src {
		resolution := normalizeSeedanceResolution(key)
		if !isFiniteNonNegative(value) {
			return nil, fmt.Errorf("invalid price for %s", resolution)
		}
		dst[resolution] = value
	}
	return dst, nil
}

func isFiniteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func normalizeSeedanceResolution(resolution string) string {
	res := strings.ToLower(strings.TrimSpace(resolution))
	switch res {
	case "4k", "2160p":
		return "4k"
	case "1080p":
		return "1080p"
	case "720p":
		return "720p"
	case "480p":
		return "480p"
	default:
		return "720p"
	}
}

func lookupSeedanceCell(prices SeedanceModelPrice, resolution string, hasVideo bool) (float64, bool) {
	bucket := prices.Text
	if hasVideo {
		bucket = prices.Video
	}
	if value, ok := bucket[resolution]; ok && value > 0 {
		return value, true
	}
	if resolution == "480p" || resolution == "720p" {
		other := "480p"
		if resolution == "480p" {
			other = "720p"
		}
		if value, ok := bucket[other]; ok && value > 0 {
			return value, true
		}
	}
	if value := seedanceBaselinePrice(SeedanceModelPrice{Text: bucket}); value > 0 {
		return value, true
	}
	return 0, false
}

func seedanceBaselinePrice(prices SeedanceModelPrice) float64 {
	if value := prices.Text["720p"]; value > 0 {
		return value
	}
	if value := prices.Text["480p"]; value > 0 {
		return value
	}
	for _, value := range prices.Text {
		if value > 0 {
			return value
		}
	}
	return 0
}

func ResolveSeedanceModel(modelNames ...string) (SeedanceModelPrice, string, bool) {
	prices := currentSeedancePrices()
	seen := make(map[string]struct{}, len(modelNames))
	for _, name := range modelNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		if price, ok := prices[name]; ok {
			return price, name, true
		}
	}

	bestKey := ""
	var bestPrice SeedanceModelPrice
	for _, name := range modelNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for key, price := range prices {
			if name == key || strings.HasPrefix(name, key+"-") || strings.HasPrefix(name, key+"_") {
				if len(key) > len(bestKey) {
					bestKey = key
					bestPrice = price
				}
			}
		}
	}
	if bestKey == "" {
		return SeedanceModelPrice{}, "", false
	}
	return bestPrice, bestKey, true
}

func HasSeedancePrice(modelNames ...string) bool {
	_, _, ok := ResolveSeedanceModel(modelNames...)
	return ok
}

func GetSeedanceUnitPriceRMB(modelName, resolution string, hasVideo bool) (float64, bool) {
	prices, _, ok := ResolveSeedanceModel(modelName)
	if !ok {
		return 0, false
	}
	return lookupSeedanceCell(prices, normalizeSeedanceResolution(resolution), hasVideo)
}

func LookupSeedanceUnitPriceRMB(resolution string, hasVideo bool, modelNames ...string) (float64, string, bool) {
	prices, matched, ok := ResolveSeedanceModel(modelNames...)
	if !ok {
		return 0, "", false
	}
	unit, found := lookupSeedanceCell(prices, normalizeSeedanceResolution(resolution), hasVideo)
	if !found {
		return 0, matched, false
	}
	return unit, matched, true
}

func SeedanceTokensPerSecond(resolution string) float64 {
	size, ok := seedanceFrameSize[normalizeSeedanceResolution(resolution)]
	if !ok {
		size = seedanceFrameSize["720p"]
	}
	return float64(size[0]*size[1]*SeedanceFrameRate) / 1024.0
}

func SeedancePerSecondRMB(unitPriceRMB float64, resolution string) float64 {
	if unitPriceRMB <= 0 {
		return 0
	}
	return SeedanceTokensPerSecond(resolution) / 1_000_000 * unitPriceRMB
}

func SeedanceModelRatio(unitPriceRMB float64) float64 {
	return usdPerMillion(unitPriceRMB / siteUSDExchangeRate())
}

// siteUSDExchangeRate is the admin-configured USD:CNY ratio used to convert
// Seedance RMB list prices into system USD / quota. Falls back to USD2RMB.
func siteUSDExchangeRate() float64 {
	rate := operation_setting.USDExchangeRate
	if rate <= 0 {
		return USD2RMB
	}
	return rate
}

// SeedanceMediaKitPolicy maps a client-facing MediaKit resolution to the Ark
// generation resolution (source) and MediaKit enhance target (final output).
// 1080p-final has two sources, so source cannot be inferred from output alone.
func SeedanceMediaKitPolicy(clientResolution string) (source, target string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(clientResolution)) {
	case "480p":
		return "480p", "720p", true
	case "720p":
		return "480p", "1080p", true
	case "1080p":
		return "720p", "1080p", true
	default:
		return "", "", false
	}
}

// SeedanceSourceResolution maps a client-facing MediaKit option to Ark generation resolution.
func SeedanceSourceResolution(clientResolution string) string {
	source, _, ok := SeedanceMediaKitPolicy(clientResolution)
	if !ok {
		return ""
	}
	return source
}

func SeedanceSuperResolutionPriceRMB(outputResolution string) (float64, bool) {
	sr := currentSeedanceRuntime().SuperResolution
	switch normalizeSeedanceResolution(outputResolution) {
	case "720p":
		if sr.From480To720 > 0 {
			return sr.From480To720, true
		}
	case "1080p":
		if sr.From720To1080 > 0 {
			return sr.From720To1080, true
		}
	}
	return 0, false
}

func CurrentSeedanceSuperResolution() SeedanceSuperResolutionPrice {
	return currentSeedanceRuntime().SuperResolution
}

func QuotaFromRMB(rmb, groupRatio float64) (int, *common.QuotaClamp) {
	return common.QuotaFromFloatChecked(rmb / siteUSDExchangeRate() * common.QuotaPerUnit * groupRatio)
}

type SeedanceQuoteInput struct {
	ModelNames        []string
	BillingResolution string
	OutputResolution  string
	HasVideo          bool
	DurationSeconds   float64
	SuperResolution   bool
	GroupRatio        float64
}

func normalizeQuoteDuration(duration float64) float64 {
	if duration <= 0 {
		return SeedanceDefaultDurationSeconds
	}
	return duration
}

func BuildSeedanceSnapshot(in SeedanceQuoteInput) (types.SeedanceBillingSnapshot, bool) {
	billingRaw := strings.TrimSpace(in.BillingResolution)
	outputRaw := strings.TrimSpace(in.OutputResolution)
	billingResolution := ""
	outputResolution := ""
	if in.SuperResolution {
		if billingRaw != "" && outputRaw != "" {
			// Source and final are stored independently: 1080p-final can be 480→1080 or 720→1080.
			billingResolution = normalizeSeedanceResolution(billingRaw)
			outputResolution = normalizeSeedanceResolution(outputRaw)
		} else if source, target, ok := SeedanceMediaKitPolicy(firstNonEmpty(outputRaw, billingRaw)); ok {
			billingResolution = source
			outputResolution = target
		}
	}
	if billingResolution == "" {
		billingResolution = normalizeSeedanceResolution(billingRaw)
		if outputRaw == "" {
			outputResolution = billingResolution
		} else {
			outputResolution = normalizeSeedanceResolution(outputRaw)
		}
	}

	unitPrice, matched, ok := LookupSeedanceUnitPriceRMB(billingResolution, in.HasVideo, in.ModelNames...)
	if !ok {
		return types.SeedanceBillingSnapshot{}, false
	}

	snap := types.SeedanceBillingSnapshot{
		UnitPriceRMB:      unitPrice,
		BillingResolution: billingResolution,
		OutputResolution:  outputResolution,
		HasVideo:          in.HasVideo,
		SuperResolution:   in.SuperResolution,
		DurationSeconds:   normalizeQuoteDuration(in.DurationSeconds),
		TokensPerSecond:   SeedanceTokensPerSecond(billingResolution),
		MatchedModel:      matched,
	}
	if in.SuperResolution {
		if srPrice, found := SeedanceSuperResolutionPriceRMB(outputResolution); found {
			snap.SuperResolutionRMB = srPrice
		}
	}
	return snap, true
}

func SeedanceCostRMB(snap types.SeedanceBillingSnapshot, tokens int, durationSeconds float64) float64 {
	duration := durationSeconds
	if duration <= 0 {
		if tokens > 0 && snap.TokensPerSecond > 0 {
			duration = float64(tokens) / snap.TokensPerSecond
		} else {
			duration = normalizeQuoteDuration(snap.DurationSeconds)
		}
	}
	tokenCount := float64(tokens)
	if tokenCount <= 0 {
		tokenCount = duration * snap.TokensPerSecond
	}
	cost := tokenCount / 1_000_000 * snap.UnitPriceRMB
	if snap.SuperResolution && snap.SuperResolutionRMB > 0 {
		cost += duration * snap.SuperResolutionRMB
	}
	return cost
}

func EstimateSeedanceQuota(in SeedanceQuoteInput) (int, types.SeedanceBillingSnapshot, *common.QuotaClamp, bool) {
	snap, ok := BuildSeedanceSnapshot(in)
	if !ok {
		return 0, types.SeedanceBillingSnapshot{}, nil, false
	}
	quota, clamp := QuotaFromRMB(SeedanceCostRMB(snap, 0, snap.DurationSeconds), in.GroupRatio)
	return quota, snap, clamp, true
}

func SettleSeedanceQuota(snap types.SeedanceBillingSnapshot, tokens int, durationSeconds, groupRatio float64) (int, *common.QuotaClamp) {
	return QuotaFromRMB(SeedanceCostRMB(snap, tokens, durationSeconds), groupRatio)
}

func BuildSeedancePublicPricing(modelNames []string, superResolution bool) (*types.SeedancePublicPricing, bool) {
	prices, _, ok := ResolveSeedanceModel(modelNames...)
	if !ok {
		return nil, false
	}

	textRMB := make(map[string]float64)
	videoRMB := make(map[string]float64)
	tokensPerSecond := map[string]float64{
		"480p":  SeedanceTokensPerSecond("480p"),
		"720p":  SeedanceTokensPerSecond("720p"),
		"1080p": SeedanceTokensPerSecond("1080p"),
		"4k":    SeedanceTokensPerSecond("4k"),
	}
	for _, resolution := range []string{"480p", "720p", "1080p", "4k"} {
		if unit, found := lookupSeedanceCell(prices, resolution, false); found {
			textRMB[resolution] = SeedancePerSecondRMB(unit, resolution)
		}
		if unit, found := lookupSeedanceCell(prices, resolution, true); found {
			videoRMB[resolution] = SeedancePerSecondRMB(unit, resolution)
		}
	}

	public := &types.SeedancePublicPricing{
		SuperResolution:   superResolution,
		TokensPerSecond:   tokensPerSecond,
		TextUnitPriceRMB:  cloneFloatMap(prices.Text),
		VideoUnitPriceRMB: cloneFloatMap(prices.Video),
		TextPerSecondRMB:  textRMB,
		VideoPerSecondRMB: videoRMB,
	}
	if !superResolution {
		return public, true
	}

	sr := CurrentSeedanceSuperResolution()
	public.SRFrom480To720RMB = sr.From480To720
	public.SRFrom720To1080RMB = sr.From720To1080
	public.OutputTextPerSecondRMB = seedanceOutputPerSecondRMB(prices, false, sr)
	public.OutputVideoPerSecondRMB = seedanceOutputPerSecondRMB(prices, true, sr)
	return public, true
}

func seedanceOutputPerSecondRMB(prices SeedanceModelPrice, hasVideo bool, sr SeedanceSuperResolutionPrice) map[string]float64 {
	out := make(map[string]float64, 3)
	unit480, has480 := lookupSeedanceCell(prices, "480p", hasVideo)
	unit720, has720 := lookupSeedanceCell(prices, "720p", hasVideo)
	if has480 && sr.From480To720 > 0 {
		out["480p"] = SeedancePerSecondRMB(unit480, "480p") + sr.From480To720
	}
	if has480 && sr.From720To1080 > 0 {
		out["720p"] = SeedancePerSecondRMB(unit480, "480p") + sr.From720To1080
	}
	if has720 && sr.From720To1080 > 0 {
		out["1080p"] = SeedancePerSecondRMB(unit720, "720p") + sr.From720To1080
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// GetVideoBillingRatio is kept for older callers. It now returns 1 when the
// model is priced in the Seedance table; actual selling prices are absolute.
func GetVideoBillingRatio(modelName, resolution string, hasVideo bool) (float64, bool) {
	_, ok := GetSeedanceUnitPriceRMB(modelName, resolution, hasVideo)
	if !ok {
		return 0, false
	}
	return 1, true
}

func LookupSeedanceBillingRatio(resolution string, hasVideo bool, modelNames ...string) (float64, bool) {
	_, _, ok := LookupSeedanceUnitPriceRMB(resolution, hasVideo, modelNames...)
	if !ok {
		return 0, false
	}
	return 1, true
}
