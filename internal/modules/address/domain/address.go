package domain

type Address struct {
	ID               string  `db:"address_id" json:"id"`
	StreetName       string  `db:"streetname_fr" json:"streetName"`
	HouseNumber      string  `db:"house_number" json:"houseNumber"`
	BoxNumber        *string `db:"box_number" json:"boxNumber"`
	MunicipalityName string  `db:"municipality_name_fr" json:"municipalityName"`
	Postcode         string  `db:"postcode" json:"postcode"`
	Distance         float64 `db:"distance" json:"distance"`
}

type Street struct {
	ID               string `db:"street_id" json:"id"`
	StreetName       string `db:"streetname_fr" json:"streetName"`
	MunicipalityName string `db:"municipality_name_fr" json:"municipalityName"`
	Postcode         string `db:"postcode" json:"postcode"`
}
