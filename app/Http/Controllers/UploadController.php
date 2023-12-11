<?php

namespace App\Http\Controllers;

use App\Models\Product;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Http\UploadedFile;
use Illuminate\Support\Facades\App;
use Imagick;
use ImagickException;

class UploadController extends Controller
{
    /**
     * @throws ImagickException
     */
    public function uploadImage(Request $request): JsonResponse
    {
        $request->validate([
            'image' => 'required|image|mimes:jpeg,png,jpg,gif|max:2048',
            'product_id' => 'required|uuid|exists:products,id',
        ]);

        /** @var Product $product */
        $product = Product::query()->findOrFail($request->product_id);

        /** @var UploadedFile $originalImage */
        $originalImage = $request->file('image');
        $timeSuffix = '-'.time();
        $baseFileName = $product->slug.$timeSuffix; // Name without extension

        $formats = ['avif', 'webp', 'png'];
        foreach ($formats as $format) {
            $fileName = $baseFileName.'.'.$format;

            $s3 = App::make('aws')->createClient('s3');
            /** @phpstan-ignore-next-line */
            $s3->putObject([
                'Bucket' => 'tsb-storage',
                'Key' => 'images/'.$fileName,
                'SourceFile' => $originalImage->getPathname(),
            ]);

            // Create and save the thumbnail using Imagick
            $thumbnail = new Imagick();
            /** @var string $image */
            $image = file_get_contents($originalImage->getPathname());
            $thumbnail->readImageBlob($image);
            $thumbnail->thumbnailImage(200, 0); // 200px width, and maintain aspect ratio
            $thumbnail->setImageFormat($format);

            // Save the thumbnail to S3
            /** @phpstan-ignore-next-line */
            $s3->putObject([
                'Bucket' => 'tsb-storage',
                'Key' => 'images/thumbnails/'.$fileName,
                'SourceFile' => $originalImage->getPathname(),
            ]);
        }

        // Save only one record without the extension
        $product->attachments()->create([
            'path' => $baseFileName,
        ]);

        return response()->json([
            'success' => true,
            'message' => 'Image and thumbnails saved in multiple formats, one attachment record created.',
        ]);
    }
}
