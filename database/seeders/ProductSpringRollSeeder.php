<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductSpringRollSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Spring roll')
            ->firstOrFail()->product_category_id;

        $products = [
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
                'price' => 5.90,
                'code' => 'D1',
                'is_active' => true,
                'slug' => 'spring-roll-saumon-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Thon avocat',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tuna avocado',
                        ],
                    ],
                ],
                'price' => 6.30,
                'code' => 'D2',
                'is_active' => true,
                'slug' => 'spring-roll-thon-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon fumé cheese ciboulette',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Smoked salmon cheese chives',
                        ],
                    ],
                ],
                'price' => 7.20,
                'code' => 'D3',
                'is_active' => true,
                'slug' => 'spring-roll-saumon-fume-cheese-ciboulette',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Poulet pané mayonnaise',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Breaded chicken mayonnaise',
                        ],
                    ],
                ],
                'price' => 6.50,
                'code' => 'D4',
                'is_active' => true,
                'slug' => 'spring-roll-poulet-pane-mayonnaise',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon mangue',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon mango',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'D5',
                'is_active' => true,
                'slug' => 'spring-roll-saumon-mangue',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tempura crevette oignons frits',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tempura shrimp fried onions',
                        ],
                    ],
                ],
                'price' => 6.90,
                'code' => 'D6',
                'is_active' => true,
                'slug' => 'spring-roll-tempura-crevette-oignons-frits',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Poulet mangue menthe',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Chicken mango mint',
                        ],
                    ],
                ],
                'price' => 7.20,
                'code' => 'D7',
                'is_active' => true,
                'slug' => 'spring-roll-poulet-mangue-menthe',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Foie gras mangue',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Foie gras mango',
                        ],
                    ],
                ],
                'price' => 9.20,
                'code' => 'D8',
                'is_active' => true,
                'slug' => 'spring-roll-foie-gras-mangue',
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
                'price' => 5.90,
                'code' => 'D9',
                'is_active' => true,
                'slug' => 'spring-roll-saumon-cheese',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'spring-roll-saumon-avocat')->exists()) {
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
