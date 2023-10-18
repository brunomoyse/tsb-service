<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductSpringRollSeeder extends Seeder
{
    public function run()
    {
        $productTag = ProductTagTranslation::query()
            ->where('locale', 'FR')
            ->where('name', 'Spring roll')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Saumon avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon avocado',
                        ],
                    ],
                ],
                'price' => 5.90,
                'code' => 'D1',
                'is_active' => true,
                'slug' => 'spring-roll-saumon-avocat',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Thon avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tuna avocado',
                        ],
                    ],
                ],
                'price' => 6.30,
                'code' => 'D2',
                'is_active' => true,
                'slug' => 'spring-roll-thon-avocat',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Saumon fumé cheese ciboulette',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Smoked salmon cheese chives',
                        ],
                    ],
                ],
                'price' => 7.20,
                'code' => 'D3',
                'is_active' => true,
                'slug' => 'spring-roll-saumon-fume-cheese-ciboulette',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Poulet pané mayonnaise',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Breaded chicken mayonnaise',
                        ],
                    ],
                ],
                'price' => 6.50,
                'code' => 'D4',
                'is_active' => true,
                'slug' => 'spring-roll-poulet-pane-mayonnaise',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Saumon mangue',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon mango',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'D5',
                'is_active' => true,
                'slug' => 'spring-roll-saumon-mangue',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Tempura crevette oignons frits',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tempura shrimp fried onions',
                        ],
                    ],
                ],
                'price' => 6.90,
                'code' => 'D6',
                'is_active' => true,
                'slug' => 'spring-roll-tempura-crevette-oignons-frits',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Poulet mangue menthe',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Chicken mango mint',
                        ],
                    ],
                ],
                'price' => 7.20,
                'code' => 'D7',
                'is_active' => true,
                'slug' => 'spring-roll-poulet-mangue-menthe',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Foie gras mangue',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Foie gras mango',
                        ],
                    ],
                ],
                'price' => 9.20,
                'code' => 'D8',
                'is_active' => true,
                'slug' => 'spring-roll-foie-gras-mangue',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Saumon cheese',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon cheese',
                        ],
                    ],
                ],
                'price' => 5.90,
                'code' => 'D9',
                'is_active' => true,
                'slug' => 'spring-roll-saumon-cheese',
                'productTags' => [
                    'connect' => [$productTag],
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
                $productItem->productTags()->sync($product['productTags']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
