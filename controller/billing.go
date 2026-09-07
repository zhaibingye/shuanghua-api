package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func convertQuotaToDisplayAmount(quota int) float64 {
	amount := float64(quota)
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		return amount / common.QuotaPerUnit * operation_setting.USDExchangeRate
	case operation_setting.QuotaDisplayTypeCustom:
		usd := amount / common.QuotaPerUnit
		rate := operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate
		if rate <= 0 {
			rate = 1
		}
		return usd * rate
	case operation_setting.QuotaDisplayTypeTokens:
		return amount
	default:
		return amount / common.QuotaPerUnit
	}
}

func GetSubscription(c *gin.Context) {
	var remainQuota int
	var usedQuota int
	var err error
	var token *model.Token
	var expiredTime int64
	if common.DisplayTokenStatEnabled {
		tokenId := c.GetInt("token_id")
		token, err = model.GetTokenById(tokenId)
		expiredTime = token.ExpiredTime
		remainQuota = token.RemainQuota
		usedQuota = token.UsedQuota
	} else {
		userId := c.GetInt("id")
		remainQuota, err = model.GetUserQuota(userId, false)
		if err == nil {
			usedQuota, err = model.GetUserUsedQuota(userId)
		}
	}
	if expiredTime <= 0 {
		expiredTime = 0
	}
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "upstream_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	quota := remainQuota + usedQuota
	amount := convertQuotaToDisplayAmount(quota)
	if token != nil && token.UnlimitedQuota {
		amount = 100000000
	}
	subscription := OpenAISubscriptionResponse{
		Object:             "billing_subscription",
		HasPaymentMethod:   true,
		SoftLimitUSD:       amount,
		HardLimitUSD:       amount,
		SystemHardLimitUSD: amount,
		AccessUntil:        expiredTime,
	}
	c.JSON(http.StatusOK, subscription)
}

func GetUsage(c *gin.Context) {
	var quota int
	var err error
	var token *model.Token
	if common.DisplayTokenStatEnabled {
		tokenId := c.GetInt("token_id")
		token, err = model.GetTokenById(tokenId)
		quota = token.UsedQuota
	} else {
		userId := c.GetInt("id")
		quota, err = model.GetUserUsedQuota(userId)
	}
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "new_api_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	amount := convertQuotaToDisplayAmount(quota)
	usage := OpenAIUsageResponse{
		Object:     "list",
		TotalUsage: amount * 100,
	}
	c.JSON(http.StatusOK, usage)
}

func GetCredits(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		openAIError := types.OpenAIError{
			Message: "user not found",
			Type:    "new_api_error",
		}
		c.JSON(http.StatusOK, gin.H{
			"error": openAIError,
		})
		return
	}

	quota, err := model.GetUserQuota(userId, false)
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "new_api_error",
		}
		c.JSON(http.StatusOK, gin.H{
			"error": openAIError,
		})
		return
	}

	usedQuota, err := model.GetUserUsedQuota(userId)
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "new_api_error",
		}
		c.JSON(http.StatusOK, gin.H{
			"error": openAIError,
		})
		return
	}

	amount := convertQuotaToDisplayAmount(quota)
	usedAmount := convertQuotaToDisplayAmount(usedQuota)
	totalGranted := amount + usedAmount

	credits := OpenAICreditsResponse{
		Object:         "credit_summary",
		TotalGranted:   totalGranted,
		TotalUsed:      usedAmount,
		TotalAvailable: amount,
		Data: OpenAICreditsData{
			TotalGranted:   totalGranted,
			TotalUsed:      usedAmount,
			TotalAvailable: amount,
			TotalUsage:     amount,
		},
	}
	c.JSON(http.StatusOK, credits)
}
