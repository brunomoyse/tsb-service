<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductMakiSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Maki')
            ->firstOrFail()->product_category_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Concombre sésame',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Cucumber sesame',
                        ],
                    ],
                ],
                'price' => 4.20,
                'code' => 'B1',
                'is_active' => true,
                'slug' => 'maki-concombre-sesame',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Avocat',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Avocado',
                        ],
                    ],
                ],
                'price' => 4.20,
                'code' => 'B2',
                'is_active' => true,
                'slug' => 'maki-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Surimi',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Surimi',
                        ],
                    ],
                ],
                'price' => 4.30,
                'code' => 'B3',
                'is_active' => true,
                'slug' => 'maki-surimi',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Radis japonais',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Japanese radish',
                        ],
                    ],
                ],
                'price' => 4.20,
                'code' => 'B4',
                'is_active' => true,
                'slug' => 'maki-radis-japonais',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Cheese concombre',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Cucumber cheese',
                        ],
                    ],
                ],
                'price' => 4.50,
                'code' => 'B5',
                'is_active' => true,
                'slug' => 'maki-cheese-concombre',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Cheese avocat',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Avocado cheese',
                        ],
                    ],
                ],
                'price' => 4.80,
                'code' => 'B6',
                'is_active' => true,
                'slug' => 'maki-cheese-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Anguille',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Eel',
                        ],
                    ],
                ],
                'price' => 5.60,
                'code' => 'B7',
                'is_active' => true,
                'slug' => 'maki-anguille',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon',
                        ],
                    ],
                ],
                'price' => 4.70,
                'code' => 'B8',
                'is_active' => true,
                'slug' => 'maki-saumon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Thon',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tuna',
                        ],
                    ],
                ],
                'price' => 5.20,
                'code' => 'B9',
                'is_active' => true,
                'slug' => 'maki-thon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tempura crevette',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Shrimp tempura',
                        ],
                    ],
                ],
                'price' => 5.50,
                'code' => 'B10',
                'is_active' => true,
                'slug' => 'maki-tempura-crevette',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon spicy',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Spicy salmon',
                        ],
                    ],
                ],
                'price' => 4.80,
                'code' => 'B11',
                'is_active' => true,
                'slug' => 'maki-saumon-spicy',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Dorade mangue',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Sea bream mango',
                        ],
                    ],
                ],
                'price' => 5.50,
                'code' => 'B12',
                'is_active' => true,
                'slug' => 'maki-dorade-mangue',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Thon cuit spicy',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Spicy cooked tuna',
                        ],
                    ],
                ],
                'price' => 5.00,
                'code' => 'B13',
                'is_active' => true,
                'slug' => 'maki-thon-cuit-spicy',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tartare thon ciboulette',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tuna and chives tartare',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'B14',
                'is_active' => true,
                'slug' => 'maki-tartare-thon-ciboulette',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Mangue',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Mango',
                        ],
                    ],
                ],
                'price' => 5.00,
                'code' => 'B15',
                'is_active' => true,
                'slug' => 'maki-mangue',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Ciboulette cheese',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Chives cheese',
                        ],
                    ],
                ],
                'price' => 5.00,
                'code' => 'B16',
                'is_active' => true,
                'slug' => 'maki-ciboulette-cheese',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Foie gras',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Foie gras',
                        ],
                    ],
                ],
                'price' => 7.50,
                'code' => 'B17',
                'is_active' => true,
                'slug' => 'maki-foie-gras',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon roll cheese',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon roll cheese',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'B19',
                'is_active' => true,
                'slug' => 'maki-saumon-roll-cheese',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'maki-saumon')->exists()) {
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
