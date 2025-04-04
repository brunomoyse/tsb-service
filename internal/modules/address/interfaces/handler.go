package interfaces

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"tsb-service/internal/modules/address/application"
)

type AddressHandler struct {
	service application.AddressService
}

func NewAddressHandler(service application.AddressService) *AddressHandler {
	return &AddressHandler{service: service}
}

// GetStreetNamesHandler returns distinct street names.
func (h *AddressHandler) GetStreetNamesHandler(c *gin.Context) {
	// Extract the single free-text query parameter
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Query parameter 'q' is required"})
		return
	}

	streetNames, err := h.service.SearchStreetNames(c.Request.Context(), query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, streetNames)
}

// GetHouseNumbersHandler returns distinct house numbers for a given street.
func (h *AddressHandler) GetHouseNumbersHandler(c *gin.Context) {
	streetID := c.Query("id")
	if streetID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id parameter is required (street id)"})
		return
	}
	houseNumbers, err := h.service.GetDistinctHouseNumbers(c.Request.Context(), streetID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, houseNumbers)
}

// GetBoxNumbersHandler returns distinct box numbers for a given street and house number.
func (h *AddressHandler) GetBoxNumbersHandler(c *gin.Context) {
	streetID := c.Query("id")
	houseNumber := c.Query("house_number")
	if streetID == "" || houseNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id and house_number parameters are required"})
		return
	}
	boxNumbers, err := h.service.GetBoxNumbers(c.Request.Context(), streetID, houseNumber)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, boxNumbers)
}

// GetFinalAddressHandler returns the final address record based on the user's selections.
func (h *AddressHandler) GetFinalAddressHandler(c *gin.Context) {
	streetID := c.Query("street_id")
	houseNumber := c.Query("house_number")
	// Use c.Query so that if box_number is not provided, it returns ""
	boxNumberParam := c.Query("box_number")

	// If the box number is an empty string, set the pointer to nil.
	var boxNumberPtr *string = nil
	if boxNumberParam != "" {
		boxNumberPtr = &boxNumberParam
	}

	if streetID == "" || houseNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "street_id and house_number are required"})
		return
	}

	address, err := h.service.GetFinalAddress(c.Request.Context(), streetID, houseNumber, boxNumberPtr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, address)
}

func (h *AddressHandler) GetAddressByIDHandler(c *gin.Context) {
	addressID := c.Param("id")
	if addressID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Address ID is required"})
		return
	}

	address, err := h.service.GetAddressByID(c.Request.Context(), addressID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, address)
}
