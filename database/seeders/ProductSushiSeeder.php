<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;
use Illuminate\Support\Str;
use Stripe\StripeClient;

class ProductSushiSeeder extends Seeder
{
    private StripeClient $stripe;

    public function __construct()
    {
        $this->stripe = new StripeClient(config('stripe.secret_key'));
    }

    public function run()
    {
        $productTagSushi = ProductTagTranslation::query()
            ->where('language', 'FR')
            ->where('name', 'Sushi')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saumon',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Salmon',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A1',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Crevette',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Shrimp',
                        ],
                    ],
                ],
                'price' => 1.90,
                'code' => 'A2',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Thon',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Tuna',
                        ],
                    ],
                ],
                'price' => 2.20,
                'code' => 'A3',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Dorade',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Sea bream',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A4',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Omelette',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Omelette',
                        ],
                    ],
                ],
                'price' => 1.80,
                'code' => 'A5',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Octopus',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Octopus',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A6',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Dorade grillée',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Grilled sea bream',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A7',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Maquereau',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Mackerel',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A8',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Surimi',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Surimi',
                        ],
                    ],
                ],
                'price' => 1.80,
                'code' => 'A9',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Avocat',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Avodaco',
                        ],
                    ],
                ],
                'price' => 1.80,
                'code' => 'A10',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Anguille',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Eel',
                        ],
                    ],
                ],
                'price' => 2.60,
                'code' => 'A11',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saumon mi-cuit',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Semi-cooked salmon',
                        ],
                    ],
                ],
                'price' => 2.30,
                'code' => 'A12',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Thon mi-cuit',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Semi-cooked tuna',
                        ],
                    ],
                ],
                'price' => 2.60,
                'code' => 'A13',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saint-Jacques mi-cuit',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Semi-cooked Saint-Jacques',
                        ],
                    ],
                ],
                'price' => 3.60,
                'code' => 'A14',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Tofu',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Tofu',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A15',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saumon cheese',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Salmon cheese',
                        ],
                    ],
                ],
                'price' => 2.30,
                'code' => 'A16',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Foie gras',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Foie gras',
                        ],
                    ],
                ],
                'price' => 3.50,
                'code' => 'A17',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Maquereau grillé',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Grilled mackerel',
                        ],
                    ],
                ],
                'price' => 2.80,
                'code' => 'A18',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saumon cuit caramélisé',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Caramelized cooked salmon',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A19',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saumon avocat',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Salmon avocado',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A20',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],

        ];

        foreach ($products as $args) {
            try {
                $newUuid = Str::uuid();

                $frenchData = current(array_filter($args['productTranslations']['create'], function ($item) {
                    return $item['language'] === 'FR';
                }));

                $stripeProduct = $this->stripe->products->create([
                    'id' => $newUuid,
                    'name' => $frenchData['name'],
                    'active' => $args['is_active'] ?? true,
                    'default_price_data' => [
                        'currency' => 'eur',
                        'unit_amount_decimal' => $args['price'] * 100,
                        'tax_behavior' => 'inclusive',
                    ],
                ]);
            } catch (\Exception $e) {
                throw new \Exception('Error creating Stripe product: '.$e->getMessage());
            }

            try {
                // I fill the price directly from the request to avoid making a new request to get the price
                // since the price is a separate object in Stripe
                $product = Product::query()->create([
                    'id' => $stripeProduct->id,
                    'price' => $args['price'],
                    'is_active' => $stripeProduct->active,
                    'code' => $args['code'] ?? null,
                ]);

                $product->productTranslations()->createMany($args['productTranslations']['create']);
                $product->productTags()->sync($args['productTags']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
