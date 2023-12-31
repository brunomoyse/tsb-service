<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductGunkanSeeder extends Seeder
{
    public function run()
    {
        $productTag = ProductTagTranslation::query()
            ->where('locale', 'FR')
            ->where('name', 'Gunkan')
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
                'price' => 2.30,
                'code' => 'T1',
                'is_active' => true,
                'slug' => 'gunkan-saumon-avocat',
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
                'price' => 2.60,
                'code' => 'T2',
                'is_active' => true,
                'slug' => 'gunkan-thon-avocat',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Avocat cheese',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Cheese avocado',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'T3',
                'is_active' => true,
                'slug' => 'gunkan-avocat-cheese',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Oeufs de saumon',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon eggs',
                        ],
                    ],
                ],
                'price' => 2.80,
                'code' => 'C1',
                'is_active' => true,
                'slug' => 'gunkan-oeufs-de-saumon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Oeufs de poisson',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Fish eggs',
                        ],
                    ],
                ],
                'price' => 2.40,
                'code' => 'C2',
                'is_active' => true,
                'slug' => 'gunkan-oeufs-de-poisson',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Tartare thon ciboulette',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tuna-chives tartare',
                        ],
                    ],
                ],
                'price' => 2.60,
                'code' => 'C3',
                'is_active' => true,
                'slug' => 'gunkan-tartare-thon-ciboulette',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Dorade mangue',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Mango bream',
                        ],
                    ],
                ],
                'price' => 2.40,
                'code' => 'C4',
                'is_active' => true,
                'slug' => 'gunkan-dorade-mangue',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Tartare saumon cheese',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon-cheese tartare',
                        ],
                    ],
                ],
                'price' => 2.30,
                'code' => 'C5',
                'is_active' => true,
                'slug' => 'gunkan-tartare-saumon-cheese',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'gunkan-saumon-avocat')->exists()) {
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
