<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductSushiSeeder extends Seeder
{
    public function run()
    {
        $productTag = ProductTagTranslation::query()
            ->where('locale', 'FR')
            ->where('name', 'Sushi')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi saumon',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon sushi',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A1',
                'is_active' => true,
                'slug' => 'sushi-saumon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi crevette',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Shrimp sushi',
                        ],
                    ],
                ],
                'price' => 1.90,
                'code' => 'A2',
                'is_active' => true,
                'slug' => 'sushi-crevette',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi thon',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tuna sushi',
                        ],
                    ],
                ],
                'price' => 2.20,
                'code' => 'A3',
                'is_active' => true,
                'slug' => 'sushi-thon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi dorade',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Sea bream sushi',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A4',
                'is_active' => true,
                'slug' => 'sushi-dorade',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi omelette',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Omelette sushi',
                        ],
                    ],
                ],
                'price' => 1.80,
                'code' => 'A5',
                'is_active' => true,
                'slug' => 'sushi-omelette',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi octopus',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Octopus sushi',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A6',
                'is_active' => true,
                'slug' => 'sushi-octopus',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi dorade grillée',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Grilled sea bream sushi',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A7',
                'is_active' => true,
                'slug' => 'sushi-dorade-grillee',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi maquereau',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Mackerel sushi',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A8',
                'is_active' => true,
                'slug' => 'sushi-maquereau',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi surimi',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Surimi sushi',
                        ],
                    ],
                ],
                'price' => 1.80,
                'code' => 'A9',
                'is_active' => true,
                'slug' => 'sushi-surimi',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Avodaco sushi',
                        ],
                    ],
                ],
                'price' => 1.80,
                'code' => 'A10',
                'is_active' => true,
                'slug' => 'sushi-avocat',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi anguille',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Eel sushi',
                        ],
                    ],
                ],
                'price' => 2.60,
                'code' => 'A11',
                'is_active' => true,
                'slug' => 'sushi-anguille',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi saumon mi-cuit',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Semi-cooked salmon sushi',
                        ],
                    ],
                ],
                'price' => 2.30,
                'code' => 'A12',
                'is_active' => true,
                'slug' => 'sushi-saumon-mi-cuit',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi thon mi-cuit',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Semi-cooked tuna sushi',
                        ],
                    ],
                ],
                'price' => 2.60,
                'code' => 'A13',
                'is_active' => true,
                'slug' => 'sushi-thon-mi-cuit',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi Saint-Jacques mi-cuit',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Semi-cooked Saint-Jacques sushi',
                        ],
                    ],
                ],
                'price' => 3.60,
                'code' => 'A14',
                'is_active' => true,
                'slug' => 'sushi-saint-jacques-mi-cuit',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi tofu',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tofu sushi',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A15',
                'is_active' => true,
                'slug' => 'sushi-tofu',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi saumon cheese',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon cheese sushi',
                        ],
                    ],
                ],
                'price' => 2.30,
                'code' => 'A16',
                'is_active' => true,
                'slug' => 'sushi-saumon-cheese',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi foie gras',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Foie gras sushi',
                        ],
                    ],
                ],
                'price' => 3.50,
                'code' => 'A17',
                'is_active' => true,
                'slug' => 'sushi-foie-gras',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi maquereau grillé',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Grilled mackerel sushi',
                        ],
                    ],
                ],
                'price' => 2.80,
                'code' => 'A18',
                'is_active' => true,
                'slug' => 'sushi-maquereau-grille',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi saumon cuit caramélisé',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Caramelized cooked salmon sushi',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A19',
                'is_active' => true,
                'slug' => 'sushi-saumon-cuit-caramelise',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Sushi saumon avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon avocado sushi',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A20',
                'is_active' => true,
                'slug' => 'sushi-saumon-avocat',
                'productTags' => [
                    'connect' => [$productTag],
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
                $productItem->productTags()->sync($product['productTags']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
