<?php

namespace Database\Seeders;

use App\Models\ProductCategory;
use Illuminate\Database\Seeder;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Str;

class FillProductPreviewSeeder extends Seeder
{
    /**
     * Run the database seeds.
     */
    public function run(): void
    {
        $productCategories = ProductCategory::query()->with(['products', 'productCategoryTranslations'])->get();

        foreach ($productCategories as $productCategory) {
            $categoryName = $productCategory->productCategoryTranslations->where('locale', '=', 'fr')->firstOrFail()->name;
            $categorySlug = Str::slug($categoryName);
            if ($categorySlug === 'menu-plateau') {
                $categorySlug = 'menu';
            }
            if ($categorySlug === 'menu-bento-box') {
                $categorySlug = 'bento';
            }
            foreach ($productCategory->products as $product) {
                $product->load('preview');
                if (isset($product->preview)) {
                    break;
                }
                $imagePath = storage_path('app/public/images/menu/'.$categorySlug.'/'.$product->slug.'.png');

                // Make the HTTP request
                $response = Http::attach(
                    'image', file_get_contents($imagePath), $product->slug.'.png'
                )->post('http://localhost:8080/api/upload', [
                    'product_id' => $product->id,
                ]);
                // Check the response
                if ($response->successful()) {
                    echo "Upload successful! \n";
                } else {
                    echo 'Upload failed!'.$product->id;
                }
            }
        }
    }
}
