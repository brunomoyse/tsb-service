<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductGunkanSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Gunkan')
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
                'price' => 2.30,
                'code' => 'T1',
                'is_active' => true,
                'slug' => 'gunkan-saumon-avocat',
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
                'price' => 2.60,
                'code' => 'T2',
                'is_active' => true,
                'slug' => 'gunkan-thon-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Avocat cheese',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Cheese avocado',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'T3',
                'is_active' => true,
                'slug' => 'gunkan-avocat-cheese',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Oeufs de saumon',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon eggs',
                        ],
                    ],
                ],
                'price' => 2.80,
                'code' => 'C1',
                'is_active' => true,
                'slug' => 'gunkan-oeufs-de-saumon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Oeufs de poisson',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Fish eggs',
                        ],
                    ],
                ],
                'price' => 2.40,
                'code' => 'C2',
                'is_active' => true,
                'slug' => 'gunkan-oeufs-de-poisson',
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
                            'name' => 'Tuna-chives tartare',
                        ],
                    ],
                ],
                'price' => 2.60,
                'code' => 'C3',
                'is_active' => true,
                'slug' => 'gunkan-tartare-thon-ciboulette',
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
                            'name' => 'Mango bream',
                        ],
                    ],
                ],
                'price' => 2.40,
                'code' => 'C4',
                'is_active' => true,
                'slug' => 'gunkan-dorade-mangue',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tartare saumon cheese',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon-cheese tartare',
                        ],
                    ],
                ],
                'price' => 2.30,
                'code' => 'C5',
                'is_active' => true,
                'slug' => 'gunkan-tartare-saumon-cheese',
                'productCategories' => [
                    'connect' => [$productCategory],
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
                $productItem->productCategories()->sync($product['productCategories']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
