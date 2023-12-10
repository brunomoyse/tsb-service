<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductSpecialRollSeeder extends Seeder
{
    public function run()
    {
        $productTag = ProductTagTranslation::query()
            ->where('locale', 'FR')
            ->where('name', 'Spécial roll')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Saumon royal',
                            'description' => 'Saumon, cheese, avocat, concombre',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Royal salmon',
                            'description' => 'Salmon, cheese, avocado, cucumber',
                        ],
                    ],
                ],
                'price' => 10.00,
                'code' => 'G1',
                'is_active' => true,
                'slug' => 'special-roll-saumon-royal',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Mangue rolls',
                            'description' => 'Poulet pané, concombre, mangue, oeufs de poissons',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Mango rolls',
                            'description' => 'Fried chicken, cuncumber, mango, fish eggs',
                        ],
                    ],
                ],
                'price' => 10.00,
                'code' => 'G2',
                'is_active' => true,
                'slug' => 'special-roll-mangue-rolls',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Avocat rolls',
                            'description' => 'Tempura crevette , sésame , oeufs de poissons',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Avocado rolls',
                            'description' => 'Shrimp tempura, sesame, fish eggs',
                        ],
                    ],
                ],
                'price' => 10.80,
                'code' => 'G3',
                'is_active' => true,
                'slug' => 'special-roll-avocat-rolls',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Anguille rolls',
                            'description' => 'Anguille, avocat, concombre, sésame',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Eel rolls',
                            'description' => 'Eel, avocado, cuncumber, sesame',
                        ],
                    ],
                ],
                'price' => 10.80,
                'code' => 'G4',
                'is_active' => true,
                'slug' => 'special-roll-anguille-rolls',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Oignon rolls',
                            'description' => 'Surimi, avocat, concombre',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Onion rolls',
                            'description' => 'Surimi, avocado, cucumber',
                        ],
                    ],
                ],
                'price' => 8.00,
                'code' => 'G5',
                'is_active' => true,
                'slug' => 'special-roll-oignon-rolls',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Miel rolls',
                            'description' => 'Saumon, miel, roquette, mangue, sésame',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Honey rolls',
                            'description' => 'Salmon, honey, salad, mango, sesame',
                        ],
                    ],
                ],
                'price' => 9.80,
                'code' => 'G6',
                'is_active' => true,
                'slug' => 'special-roll-miel-rolls',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Assortiment rolls',
                            'description' => 'Saumon, thon, tempura crevette, avocat, concombre',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Mixed rolls',
                            'description' => 'Salmon, tuna, shrimp tempura, avocado, cucumber',
                        ],
                    ],
                ],
                'price' => 10.80,
                'code' => 'G7',
                'is_active' => true,
                'slug' => 'special-roll-assortiment-rolls',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Spicy saumon',
                            'description' => 'Saumon, concombre, avocat, oignons frits, mayo, épicé',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Spicy salmon',
                            'description' => 'Salmon, cucumber, avocado, fried chicken, mayo, spicy',
                        ],
                    ],
                ],
                'price' => 11.80,
                'code' => 'G8',
                'is_active' => true,
                'slug' => 'special-roll-spicy-saumon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Crispy saumon',
                            'description' => 'Saumon, concombre, avocat, oignons frits, mayo, épicé',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Crispy salmon',
                            'description' => 'Salmon cucumber, avocado, fried chicken, mayo, spicy',
                        ],
                    ],
                ],
                'price' => 10.80,
                'code' => 'G9',
                'is_active' => true,
                'slug' => 'special-roll-crispy-saumon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Chips rolls',
                            'description' => 'Chips, poulet pané',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Chips rolls',
                            'description' => 'Chips, fried chicken',
                        ],
                    ],
                ],
                'price' => 9.80,
                'code' => 'G10',
                'is_active' => true,
                'slug' => 'special-roll-chips-rolls',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Osaka rolls',
                            'description' => 'Saumon mi-cuit, avocat, concombre, tempura crevette, oeufs de poissons, ciboulette',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Osaka rolls',
                            'description' => 'Semi-cooked salmon, avocado, cucumber, shrimp tempura, fish eggs, chive',
                        ],
                    ],
                ],
                'price' => 12.80,
                'code' => 'G11',
                'is_active' => true,
                'slug' => 'special-roll-osaka-rolls',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Foie gras fraise rolls',
                            'description' => 'Foie gras, fraise, avocat, oignons frits, miel',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Foie gras strawberry rolls',
                            'description' => 'Foie gras, strawberry, avocado, fried onions, honey',
                        ],
                    ],
                ],
                'price' => 14.80,
                'code' => 'G12',
                'is_active' => true,
                'slug' => 'special-roll-foie-gras-fraise-rolls',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Spicy poulet',
                            'description' => 'Poulet, concombre, roquette, oignons frits, sauce du chef',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Spicy chicken',
                            'description' => 'Chicken, cucumber, salad, fried onions, chef\'s sauce',
                        ],
                    ],
                ],
                'price' => 9.80,
                'code' => 'G18',
                'is_active' => true,
                'slug' => 'special-roll-spicy-poulet',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Délice mangue',
                            'description' => 'Mangue, saumon, cheese, mayonnaise japonaise',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Mango delight',
                            'description' => 'Mango, salmon, cheese, japanese mayo',
                        ],
                    ],
                ],
                'price' => 10.50,
                'code' => 'G15',
                'is_active' => true,
                'slug' => 'special-roll-delice-mangue',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'special-roll-saumon-royal')->exists()) {
            return;
        }

        foreach ($products as $product) {
            try {
                /* @var Product $productItem */
                $productItem = Product::query()->create([
                    'price' => $product['price'],
                    'is_active' => true,
                    'slug' => $product['slug'],
                    'code' => $product['code'] ?? null,
                ]);

                $productItem->productTranslations()->createMany($product['productTranslations']['create']);
                $productItem->productTags()->sync($product['productTags']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
