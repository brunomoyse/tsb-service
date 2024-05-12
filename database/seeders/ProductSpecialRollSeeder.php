<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductSpecialRollSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Spécial roll')
            ->firstOrFail()->product_category_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon royal',
                            'description' => 'Saumon, cheese, avocat, concombre',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Royal salmon',
                            'description' => 'Salmon, cheese, avocado, cucumber',
                        ],
                    ],
                ],
                'price' => 10.00,
                'code' => 'G1',
                'is_active' => true,
                'slug' => 'special-roll-saumon-royal',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Mangue rolls',
                            'description' => 'Poulet pané, concombre, mangue, oeufs de poissons',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Mango rolls',
                            'description' => 'Fried chicken, cuncumber, mango, fish eggs',
                        ],
                    ],
                ],
                'price' => 10.00,
                'code' => 'G2',
                'is_active' => true,
                'slug' => 'special-roll-mangue-rolls',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Avocat rolls',
                            'description' => 'Tempura crevette , sésame , oeufs de poissons',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Avocado rolls',
                            'description' => 'Shrimp tempura, sesame, fish eggs',
                        ],
                    ],
                ],
                'price' => 10.80,
                'code' => 'G3',
                'is_active' => true,
                'slug' => 'special-roll-avocat-rolls',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Anguille rolls',
                            'description' => 'Anguille, avocat, concombre, sésame',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Eel rolls',
                            'description' => 'Eel, avocado, cuncumber, sesame',
                        ],
                    ],
                ],
                'price' => 10.80,
                'code' => 'G4',
                'is_active' => true,
                'slug' => 'special-roll-anguille-rolls',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Oignon rolls',
                            'description' => 'Surimi, avocat, concombre',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Onion rolls',
                            'description' => 'Surimi, avocado, cucumber',
                        ],
                    ],
                ],
                'price' => 8.00,
                'code' => 'G5',
                'is_active' => true,
                'slug' => 'special-roll-oignon-rolls',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Miel rolls',
                            'description' => 'Saumon, miel, roquette, mangue, sésame',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Honey rolls',
                            'description' => 'Salmon, honey, salad, mango, sesame',
                        ],
                    ],
                ],
                'price' => 9.80,
                'code' => 'G6',
                'is_active' => true,
                'slug' => 'special-roll-miel-rolls',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Assortiment rolls',
                            'description' => 'Saumon, thon, tempura crevette, avocat, concombre',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Mixed rolls',
                            'description' => 'Salmon, tuna, shrimp tempura, avocado, cucumber',
                        ],
                    ],
                ],
                'price' => 10.80,
                'code' => 'G7',
                'is_active' => true,
                'slug' => 'special-roll-assortiment-rolls',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Spicy saumon',
                            'description' => 'Saumon, concombre, avocat, oignons frits, mayo, épicé',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Spicy salmon',
                            'description' => 'Salmon, cucumber, avocado, fried chicken, mayo, spicy',
                        ],
                    ],
                ],
                'price' => 11.80,
                'code' => 'G8',
                'is_active' => true,
                'slug' => 'special-roll-spicy-saumon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Crispy saumon',
                            'description' => 'Saumon, concombre, avocat, oignons frits, mayo, épicé',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Crispy salmon',
                            'description' => 'Salmon cucumber, avocado, fried chicken, mayo, spicy',
                        ],
                    ],
                ],
                'price' => 10.80,
                'code' => 'G9',
                'is_active' => true,
                'slug' => 'special-roll-crispy-saumon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Chips rolls',
                            'description' => 'Chips, poulet pané',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Chips rolls',
                            'description' => 'Chips, fried chicken',
                        ],
                    ],
                ],
                'price' => 9.80,
                'code' => 'G10',
                'is_active' => true,
                'slug' => 'special-roll-chips-rolls',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Osaka rolls',
                            'description' => 'Saumon mi-cuit, avocat, concombre, tempura crevette, oeufs de poissons, ciboulette',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Osaka rolls',
                            'description' => 'Semi-cooked salmon, avocado, cucumber, shrimp tempura, fish eggs, chive',
                        ],
                    ],
                ],
                'price' => 12.80,
                'code' => 'G11',
                'is_active' => true,
                'slug' => 'special-roll-osaka-rolls',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Foie gras fraise rolls',
                            'description' => 'Foie gras, fraise, avocat, oignons frits, miel',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Foie gras strawberry rolls',
                            'description' => 'Foie gras, strawberry, avocado, fried onions, honey',
                        ],
                    ],
                ],
                'price' => 14.80,
                'code' => 'G12',
                'is_active' => true,
                'slug' => 'special-roll-foie-gras-fraise-rolls',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Spicy poulet',
                            'description' => 'Poulet, concombre, roquette, oignons frits, sauce du chef',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Spicy chicken',
                            'description' => 'Chicken, cucumber, salad, fried onions, chef\'s sauce',
                        ],
                    ],
                ],
                'price' => 9.80,
                'code' => 'G18',
                'is_active' => true,
                'slug' => 'special-roll-spicy-poulet',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Délice mangue',
                            'description' => 'Mangue, saumon, cheese, mayonnaise japonaise',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Mango delight',
                            'description' => 'Mango, salmon, cheese, japanese mayo',
                        ],
                    ],
                ],
                'price' => 10.50,
                'code' => 'G15',
                'is_active' => true,
                'slug' => 'special-roll-delice-mangue',
                'productCategories' => [
                    'connect' => [$productCategory],
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
                $productItem->productCategories()->sync($product['productCategories']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
