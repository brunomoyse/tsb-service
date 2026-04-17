package interfaces

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	addressApplication "tsb-service/internal/modules/address/application"
	addressDomain "tsb-service/internal/modules/address/domain"
	"tsb-service/internal/modules/user/application"
	"tsb-service/pkg/logging"
)

type UserHandler struct {
	service        application.UserService
	addressService addressApplication.AddressService
}

func NewUserHandler(
	service application.UserService,
	addressService addressApplication.AddressService,
) *UserHandler {
	return &UserHandler{
		service:        service,
		addressService: addressService,
	}
}

func (h *UserHandler) GetUserProfileHandler(c *gin.Context) {
	userID := c.GetString("userID")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "handler: user not authenticated"})
		return
	}

	user, err := h.service.GetUserByID(c, userID)
	if err != nil {
		logging.FromContext(c.Request.Context()).Error("failed to fetch user profile", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user profile"})
		return
	}

	var address *addressDomain.Address
	if user.DefaultPlaceID != nil {
		address, _ = h.addressService.GetByPlaceID(c.Request.Context(), *user.DefaultPlaceID)
	}

	res := NewUserResponse(user, address)
	c.JSON(http.StatusOK, res)
}

func (h *UserHandler) UpdateMeHandler(c *gin.Context) {
	ctx := c.Request.Context()

	userID := c.GetString("userID")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "handler: user not authenticated"})
		return
	}

	var req UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logging.FromContext(ctx).Warn("invalid request payload", zap.String("handler", "updateMe"), zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}

	user, err := h.service.UpdateMe(ctx, userID, req.FirstName, req.LastName, req.Email, req.PhoneNumber, req.AddressID, nil, nil)
	if err != nil {
		logging.FromContext(ctx).Error("failed to update user profile", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update user profile"})
		return
	}

	var address *addressDomain.Address
	if user.DefaultPlaceID != nil {
		address, _ = h.addressService.GetByPlaceID(c.Request.Context(), *user.DefaultPlaceID)
	}

	res := NewUserResponse(user, address)
	c.JSON(http.StatusOK, res)
}
