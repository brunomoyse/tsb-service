<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductMakiSeeder extends Seeder
{
    public function run()
    {
        $productTag = ProductTagTranslation::query()
            ->where('locale', 'FR')
            ->where('name', 'Maki')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki concombre sésame',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Cucumber sesame maki',
                        ],
                    ],
                ],
                'price' => 4.20,
                'code' => 'B1',
                'is_active' => true,
                'slug' => 'maki-concombre-sesame',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Avocado maki',
                        ],
                    ],
                ],
                'price' => 4.20,
                'code' => 'B2',
                'is_active' => true,
                'slug' => 'maki-avocat',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki surimi',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Surimi maki',
                        ],
                    ],
                ],
                'price' => 4.30,
                'code' => 'B3',
                'is_active' => true,
                'slug' => 'maki-surimi',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki radis japonais',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Japanese radish maki',
                        ],
                    ],
                ],
                'price' => 4.20,
                'code' => 'B4',
                'is_active' => true,
                'slug' => 'maki-radis-japonais',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki cheese concombre',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Cucumber cheese maki',
                        ],
                    ],
                ],
                'price' => 4.50,
                'code' => 'B5',
                'is_active' => true,
                'slug' => 'maki-cheese-concombre',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki cheese avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Avocado cheese maki',
                        ],
                    ],
                ],
                'price' => 4.80,
                'code' => 'B6',
                'is_active' => true,
                'slug' => 'maki-cheese-avocat',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki anguille',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Eel maki',
                        ],
                    ],
                ],
                'price' => 5.60,
                'code' => 'B7',
                'is_active' => true,
                'slug' => 'maki-anguille',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki saumon',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon maki',
                        ],
                    ],
                ],
                'price' => 4.70,
                'code' => 'B8',
                'is_active' => true,
                'slug' => 'maki-saumon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki thon',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tuna maki',
                        ],
                    ],
                ],
                'price' => 5.20,
                'code' => 'B9',
                'is_active' => true,
                'slug' => 'maki-thon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki tempura crevette',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Shrimp tempura maki',
                        ],
                    ],
                ],
                'price' => 5.50,
                'code' => 'B10',
                'is_active' => true,
                'slug' => 'maki-tempura-crevette',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki saumon spicy',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Spicy salmon maki',
                        ],
                    ],
                ],
                'price' => 4.80,
                'code' => 'B11',
                'is_active' => true,
                'slug' => 'maki-saumon-spicy',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki dorade mangue',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Sea bream mango maki',
                        ],
                    ],
                ],
                'price' => 5.50,
                'code' => 'B12',
                'is_active' => true,
                'slug' => 'maki-dorade-mangue',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki thon cuit spicy',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Spicy cooked tuna maki',
                        ],
                    ],
                ],
                'price' => 5.00,
                'code' => 'B13',
                'is_active' => true,
                'slug' => 'maki-thon-cuit-spicy',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki tartare thon ciboulette',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tuna and chives tartare maki',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'B14',
                'is_active' => true,
                'slug' => 'maki-tartare-thon-ciboulette',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki mangue',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Mango maki',
                        ],
                    ],
                ],
                'price' => 5.00,
                'code' => 'B15',
                'is_active' => true,
                'slug' => 'maki-mangue',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki ciboulette cheese',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Chives cheese maki',
                        ],
                    ],
                ],
                'price' => 5.00,
                'code' => 'B16',
                'is_active' => true,
                'slug' => 'maki-ciboulette-cheese',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki foie gras',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Foie gras maki',
                        ],
                    ],
                ],
                'price' => 7.50,
                'code' => 'B17',
                'is_active' => true,
                'slug' => 'maki-foie-gras',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Maki saumon roll cheese',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon roll cheese maki',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'B19',
                'is_active' => true,
                'slug' => 'maki-saumon-roll-cheese',
                'productTags' => [
                    'connect' => [$productTag],
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
                $productItem->productTags()->sync($product['productTags']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
