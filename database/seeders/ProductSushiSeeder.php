<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductSushiSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Sushi')
            ->firstOrFail()->product_category_id;

        $products = [
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
                'price' => 2.00,
                'code' => 'A1',
                'is_active' => true,
                'slug' => 'sushi-saumon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Crevette',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Shrimp',
                        ],
                    ],
                ],
                'price' => 1.90,
                'code' => 'A2',
                'is_active' => true,
                'slug' => 'sushi-crevette',
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
                'price' => 2.20,
                'code' => 'A3',
                'is_active' => true,
                'slug' => 'sushi-thon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Dorade',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Sea bream',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A4',
                'is_active' => true,
                'slug' => 'sushi-dorade',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Omelette',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Omelette',
                        ],
                    ],
                ],
                'price' => 1.80,
                'code' => 'A5',
                'is_active' => true,
                'slug' => 'sushi-omelette',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Octopus',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Octopus',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A6',
                'is_active' => true,
                'slug' => 'sushi-octopus',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Dorade grillée',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Grilled sea bream',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A7',
                'is_active' => true,
                'slug' => 'sushi-dorade-grillee',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Maquereau',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Mackerel',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A8',
                'is_active' => true,
                'slug' => 'sushi-maquereau',
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
                'price' => 1.80,
                'code' => 'A9',
                'is_active' => true,
                'slug' => 'sushi-surimi',
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
                            'name' => 'Avodaco',
                        ],
                    ],
                ],
                'price' => 1.80,
                'code' => 'A10',
                'is_active' => true,
                'slug' => 'sushi-avocat',
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
                'price' => 2.60,
                'code' => 'A11',
                'is_active' => true,
                'slug' => 'sushi-anguille',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon mi-cuit',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Semi-cooked salmon',
                        ],
                    ],
                ],
                'price' => 2.30,
                'code' => 'A12',
                'is_active' => true,
                'slug' => 'sushi-saumon-mi-cuit',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Thon mi-cuit',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Semi-cooked tuna',
                        ],
                    ],
                ],
                'price' => 2.60,
                'code' => 'A13',
                'is_active' => true,
                'slug' => 'sushi-thon-mi-cuit',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saint-Jacques mi-cuit',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Semi-cooked Saint-Jacques',
                        ],
                    ],
                ],
                'price' => 3.60,
                'code' => 'A14',
                'is_active' => true,
                'slug' => 'sushi-saint-jacques-mi-cuit',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tofu',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tofu',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A15',
                'is_active' => true,
                'slug' => 'sushi-tofu',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon cheese',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon cheese',
                        ],
                    ],
                ],
                'price' => 2.30,
                'code' => 'A16',
                'is_active' => true,
                'slug' => 'sushi-saumon-cheese',
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
                'price' => 3.50,
                'code' => 'A17',
                'is_active' => true,
                'slug' => 'sushi-foie-gras',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Maquereau grillé',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Grilled mackerel',
                        ],
                    ],
                ],
                'price' => 2.80,
                'code' => 'A18',
                'is_active' => true,
                'slug' => 'sushi-maquereau-grille',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon cuit caramélisé',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Caramelized cooked salmon',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A19',
                'is_active' => true,
                'slug' => 'sushi-saumon-cuit-caramelise',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon avocat',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon avocado',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A20',
                'is_active' => true,
                'slug' => 'sushi-saumon-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],

        ];

        if (Product::query()->where('slug', 'sushi-saumon')->exists()) {
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
