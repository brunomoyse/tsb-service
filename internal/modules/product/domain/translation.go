package domain

type Translation struct {
	Language    string  `json:"locale" binding:"required"`
	Name        string  `json:"name" binding:"required"`
	Description *string `json:"description"`
}
