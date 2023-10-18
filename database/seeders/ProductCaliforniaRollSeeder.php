<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductCaliforniaRollSeeder extends Seeder
{
    public function run(): void
    {
        $productTag = ProductTagTranslation::query()
            ->where('locale', 'FR')
            ->where('name', 'Calfifornia roll')
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
                'price' => 5.60,
                'code' => 'E1',
                'is_active' => true,
                'slug' => 'california-roll-saumon-avocat',
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
                'price' => 6.20,
                'code' => 'E2',
                'is_active' => true,
                'slug' => 'california-roll-thon-avocat',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Surimi avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Surimi avocado',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'E3',
                'is_active' => true,
                'slug' => 'california-roll-surimi-avocat',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Tempura crevette avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tempura shrimp avocado',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'E4',
                'is_active' => true,
                'slug' => 'california-roll-tempura-crevette-avocat',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Tempura crevette cheese',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tempura shrimp cheese',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'E5',
                'is_active' => true,
                'slug' => 'california-roll-tempura-crevette-cheese',
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
                'price' => 5.80,
                'code' => 'E6',
                'is_active' => true,
                'slug' => 'california-roll-saumon-mangue',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Crevette avocat cheese',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Shrimp avocado cheese',
                        ],
                    ],
                ],
                'price' => 6.10,
                'code' => 'E7',
                'is_active' => true,
                'slug' => 'california-roll-crevette-avocat-cheese',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Oignons frits poulet',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Fried onions chicken',
                        ],
                    ],
                ],
                'price' => 7.00,
                'code' => 'E8',
                'is_active' => true,
                'slug' => 'california-roll-oignons-frits-poulet',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Thon cuit pomme spicy',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Cooked tuna spicy apple',
                        ],
                    ],
                ],
                'price' => 5.70,
                'code' => 'E9',
                'is_active' => true,
                'slug' => 'california-roll-thon-cuit-pomme-spicy',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Végétarien',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Vegetarian',
                        ],
                    ],
                ],
                'price' => 5.20,
                'code' => 'E10',
                'is_active' => true,
                'slug' => 'california-roll-vegetarien',
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
                'price' => 5.80,
                'code' => 'E16',
                'is_active' => true,
                'slug' => 'california-roll-saumon-cheese',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Poulet frit roquette miel',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Fried chicken salad honey',
                        ],
                    ],
                ],
                'price' => 7.10,
                'code' => 'E17',
                'is_active' => true,
                'slug' => 'california-roll-',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Oignons frits foie gras miel',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Fried onions foie gras honey',
                        ],
                    ],
                ],
                'price' => 9.80,
                'code' => 'E18',
                'is_active' => true,
                'slug' => 'california-roll-oignons-frits-foie-gras-miel',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Tempura crevette coriandre piquant',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tempura shrimp coriander spicy',
                        ],
                    ],
                ],
                'price' => 7.80,
                'code' => 'E19',
                'is_active' => true,
                'slug' => 'california-roll-tempura-crevette-coriandre-piquant',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Tempura crevette avocat cheese fraise',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tempura shrimp avocado cheese strawberry',
                        ],
                    ],
                ],
                'price' => 7.80,
                'code' => 'E20',
                'is_active' => true,
                'slug' => 'california-roll-tempura-crevette-avocat-cheese-fraise',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Poulet piquant',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Spicy chicken',
                        ],
                    ],
                ],
                'price' => 7.80,
                'code' => 'E21',
                'is_active' => true,
                'slug' => 'california-roll-poulet-piquant',
                'productTags' => [
                    'connect' => [$productTag],
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
                $productItem->productTags()->sync($product['productTags']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
