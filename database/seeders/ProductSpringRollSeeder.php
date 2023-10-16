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
                            'name' => 'Spring roll saumon avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon avocado spring roll',
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
                            'name' => 'Spring roll thon avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tuna avocado spring roll',
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
                            'name' => 'Spring roll saumon fumé cheese ciboulette',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Smoked salmon cheese chives spring roll',
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
                            'name' => 'Spring roll poulet pané mayonnaise',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Breaded chicken mayonnaise spring roll',
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
                            'name' => 'Spring roll saumon mangue',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon mango spring roll',
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
                            'name' => 'Spring roll tempura crevette oignons frits',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tempura shrimp fried onions spring roll',
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
                            'name' => 'Spring roll poulet mangue menthe',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Chicken mango mint spring roll',
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
                            'name' => 'Spring roll foie gras mangue',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Foie gras mango spring roll',
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
                            'name' => 'Spring roll saumon cheese',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon cheese spring roll',
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
