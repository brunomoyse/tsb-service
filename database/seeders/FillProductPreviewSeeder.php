<?php

namespace Database\Seeders;

use App\Models\ProductTag;
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
        $productTags = ProductTag::query()->with(['products', 'productTagTranslations'])->get();

        foreach ($productTags as $productTag) {
            $tagName = $productTag->productTagTranslations->where('locale', '=', 'FR')->firstOrFail()->name;
            $tagSlug = Str::slug($tagName);
            if ($tagSlug === 'menu-plateau') {
                $tagSlug = 'menu';
            }
            if ($tagSlug === 'menu-bento-box') {
                $tagSlug = 'bento';
            }
            foreach ($productTag->products as $product) {
                $product->load('preview');
                if (isset($product->preview)) {
                    break;
                }
                $imagePath = storage_path('app/public/images/menu/'.$tagSlug.'/'.$product->slug.'.png');

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
