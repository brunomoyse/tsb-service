<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductCaliforniaRollSeeder extends Seeder
{
    public function run(): void
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'California roll')
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
                'price' => 5.60,
                'code' => 'E1',
                'is_active' => true,
                'slug' => 'california-roll-saumon-avocat',
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
                'price' => 6.20,
                'code' => 'E2',
                'is_active' => true,
                'slug' => 'california-roll-thon-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Surimi avocat',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Surimi avocado',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'E3',
                'is_active' => true,
                'slug' => 'california-roll-surimi-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tempura crevette avocat',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tempura shrimp avocado',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'E4',
                'is_active' => true,
                'slug' => 'california-roll-tempura-crevette-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tempura crevette cheese',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tempura shrimp cheese',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'E5',
                'is_active' => true,
                'slug' => 'california-roll-tempura-crevette-cheese',
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
                'price' => 5.80,
                'code' => 'E6',
                'is_active' => true,
                'slug' => 'california-roll-saumon-mangue',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Crevette avocat cheese',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Shrimp avocado cheese',
                        ],
                    ],
                ],
                'price' => 6.10,
                'code' => 'E7',
                'is_active' => true,
                'slug' => 'california-roll-crevette-avocat-cheese',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Oignons frits poulet',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Fried onions chicken',
                        ],
                    ],
                ],
                'price' => 7.00,
                'code' => 'E8',
                'is_active' => true,
                'slug' => 'california-roll-oignons-frits-poulet',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Thon cuit pomme spicy',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Cooked tuna spicy apple',
                        ],
                    ],
                ],
                'price' => 5.70,
                'code' => 'E9',
                'is_active' => true,
                'slug' => 'california-roll-thon-cuit-pomme-spicy',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Végétarien',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Vegetarian',
                        ],
                    ],
                ],
                'price' => 5.20,
                'code' => 'E10',
                'is_active' => true,
                'slug' => 'california-roll-vegetarien',
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
                'price' => 5.80,
                'code' => 'E16',
                'is_active' => true,
                'slug' => 'california-roll-saumon-cheese',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Poulet frit roquette miel',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Fried chicken salad honey',
                        ],
                    ],
                ],
                'price' => 7.10,
                'code' => 'E17',
                'is_active' => true,
                'slug' => 'california-roll-poulet-frit-roquette-miel',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Oignons frits foie gras miel',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Fried onions foie gras honey',
                        ],
                    ],
                ],
                'price' => 9.80,
                'code' => 'E18',
                'is_active' => true,
                'slug' => 'california-roll-oignons-frits-foie-gras-miel',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tempura crevette coriandre piquant',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tempura shrimp coriander spicy',
                        ],
                    ],
                ],
                'price' => 7.80,
                'code' => 'E19',
                'is_active' => true,
                'slug' => 'california-roll-tempura-crevette-coriandre-piquant',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tempura crevette avocat cheese fraise',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tempura shrimp avocado cheese strawberry',
                        ],
                    ],
                ],
                'price' => 7.80,
                'code' => 'E20',
                'is_active' => true,
                'slug' => 'california-roll-tempura-crevette-avocat-cheese-fraise',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Poulet piquant',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Spicy chicken',
                        ],
                    ],
                ],
                'price' => 7.80,
                'code' => 'E21',
                'is_active' => true,
                'slug' => 'california-roll-poulet-piquant',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'california-roll-saumon-avocat')->exists()) {
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
